package agent

import (
	"errors"
	"strings"
	"unicode"
)

// errStreamRepetition aborts a single streaming step early when the model
// enters a degenerative text-repetition loop.
var errStreamRepetition = errors.New("model entered a degenerative text repetition loop")

const (
	// maxRepetitionNudges bounds how many times the loop nudges a model that
	// fell into a repetitive text-only loop instead of calling a tool or answering.
	maxRepetitionNudges = 1
	// streamRepetitionMinLen avoids scanning short responses on every delta.
	streamRepetitionMinLen = 220
)

// splitProseSentences extracts non-empty sentence-like segments outside fenced
// code blocks. It handles missing spaces after sentence-ending punctuation
// (e.g. "malam ini.Saya cek") while avoiding splits inside decimals/URLs.
func splitProseSentences(text string) []string {
	var sentences []string
	var cur strings.Builder
	inCode := false
	runes := []rune(text)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '`' && i+2 < len(runes) && runes[i+1] == '`' && runes[i+2] == '`' {
			inCode = !inCode
			cur.Reset()
			i += 2
			continue
		}
		if inCode {
			continue
		}
		if r == '\n' {
			if s := strings.TrimSpace(cur.String()); s != "" {
				sentences = append(sentences, s)
			}
			cur.Reset()
			continue
		}
		cur.WriteRune(r)
		if r == '.' || r == '!' || r == '?' {
			// Split on sentence punctuation followed by whitespace OR an
			// immediately glued uppercase letter (common in stuttering loops
			// like "...malam ini.Saya cek...").
			if i+1 >= len(runes) {
				if s := strings.TrimSpace(cur.String()); s != "" {
					sentences = append(sentences, s)
				}
				cur.Reset()
				continue
			}
			next := runes[i+1]
			prevLower := i > 0 && unicode.IsLetter(runes[i-1])
			if unicode.IsSpace(next) || (prevLower && unicode.IsUpper(next)) {
				if s := strings.TrimSpace(cur.String()); s != "" {
					sentences = append(sentences, s)
				}
				cur.Reset()
			}
		}
	}
	if s := strings.TrimSpace(cur.String()); s != "" {
		sentences = append(sentences, s)
	}
	return sentences
}

func normalizeSentenceWords(sentence string) ([]string, string) {
	var clean strings.Builder
	var words []string
	var word strings.Builder
	flushWord := func() {
		if word.Len() > 0 {
			w := word.String()
			words = append(words, w)
			if clean.Len() > 0 {
				clean.WriteByte(' ')
			}
			clean.WriteString(w)
			word.Reset()
		}
	}
	for _, r := range strings.ToLower(sentence) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			word.WriteRune(r)
		} else {
			flushWord()
		}
	}
	flushWord()
	return words, clean.String()
}

func wordJaccard(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	setA := make(map[string]struct{}, len(a))
	for _, w := range a {
		setA[w] = struct{}{}
	}
	setB := make(map[string]struct{}, len(b))
	for _, w := range b {
		setB[w] = struct{}{}
	}
	inter := 0
	for w := range setA {
		if _, ok := setB[w]; ok {
			inter++
		}
	}
	union := len(setA) + len(setB) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// detectDegenerativeRepetition reports whether text contains a clear model
// stuttering loop (the same or near-identical sentence repeated 3+ times).
func detectDegenerativeRepetition(text string) bool {
	if len(text) < 80 {
		return false
	}
	sentences := splitProseSentences(text)
	if len(sentences) < 3 {
		return false
	}

	type entry struct {
		words []string
		norm  string
	}
	var seen []entry
	exactCounts := map[string]int{}
	nearDupCount := 0

	for _, s := range sentences {
		words, norm := normalizeSentenceWords(s)
		if len(words) < 4 || len(norm) < 16 {
			continue
		}
		exactCounts[norm]++
		if exactCounts[norm] >= 3 {
			return true
		}
		matched := false
		for _, prev := range seen {
			if prev.norm == norm || wordJaccard(prev.words, words) >= 0.70 {
				matched = true
				break
			}
		}
		if matched {
			nearDupCount++
			if nearDupCount >= 3 {
				return true
			}
		} else {
			seen = append(seen, entry{words: words, norm: norm})
		}
	}
	return false
}

// streamRepetitionLoop is a stricter mid-stream trigger used to abort a runaway
// generation before it burns thousands of tokens.
func streamRepetitionLoop(text string) bool {
	if len(text) < streamRepetitionMinLen {
		return false
	}
	sentences := splitProseSentences(text)
	if len(sentences) < 5 {
		return false
	}
	type entry struct {
		words []string
		norm  string
	}
	var seen []entry
	nearDupCount := 0
	for _, s := range sentences {
		words, norm := normalizeSentenceWords(s)
		if len(words) < 4 || len(norm) < 16 {
			continue
		}
		matched := false
		for _, prev := range seen {
			if prev.norm == norm || wordJaccard(prev.words, words) >= 0.70 {
				matched = true
				break
			}
		}
		if matched {
			nearDupCount++
			if nearDupCount >= 4 {
				return true
			}
		} else {
			seen = append(seen, entry{words: words, norm: norm})
		}
	}
	return false
}

// CollapseRepeatedProse removes repeated or near-identical stuttering sentences
// from prose outside fenced code blocks while keeping the first occurrence and
// fixing missing spaces after sentence punctuation (e.g. "ini.Saya").
func CollapseRepeatedProse(text string) string {
	if strings.TrimSpace(text) == "" {
		return text
	}
	type keptEntry struct {
		words []string
		norm  string
	}
	var seen []keptEntry

	var out strings.Builder
	lines := strings.Split(text, "\n")
	inCode := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inCode = !inCode
			out.WriteString(line)
			if i < len(lines)-1 {
				out.WriteByte('\n')
			}
			continue
		}
		if inCode {
			out.WriteString(line)
			if i < len(lines)-1 {
				out.WriteByte('\n')
			}
			continue
		}
		sentences := splitProseSentences(line)
		if len(sentences) == 0 {
			out.WriteString(line)
		} else {
			var kept []string
			for _, s := range sentences {
				words, norm := normalizeSentenceWords(s)
				if len(words) >= 5 && len(norm) >= 22 {
					dup := false
					for _, prev := range seen {
						if prev.norm == norm || wordJaccard(prev.words, words) >= 0.70 {
							dup = true
							break
						}
					}
					if dup {
						continue
					}
					seen = append(seen, keptEntry{words: words, norm: norm})
				}
				kept = append(kept, s)
			}
			if len(kept) > 0 {
				out.WriteString(strings.Join(kept, " "))
			} else if len(sentences) > 0 && strings.TrimSpace(line) == "" {
				out.WriteString(line)
			}
		}
		if i < len(lines)-1 {
			out.WriteByte('\n')
		}
	}
	return strings.TrimSpace(out.String())
}
