package agent

import (
	"strings"
	"testing"
)

// schemaProblems walks every registered tool schema and reports the two ways a
// hand-built schema goes wrong: a required entry that names nothing, and a
// required entry with no matching property.
//
// The required list is a variadic of strings, so an empty call such as
// object(props, "") silently declares a required property named "" that no
// caller can ever satisfy.
func schemaProblems(t *testing.T) []string {
	t.Helper()
	var problems []string
	for _, tool := range DefaultRegistry().Tools() {
		schema := tool.Schema()
		if schema == nil {
			problems = append(problems, tool.Name()+": nil schema")
			continue
		}
		properties, _ := schema["properties"].(map[string]any)
		raw, present := schema["required"]
		if !present {
			continue
		}
		required, ok := raw.([]string)
		if !ok {
			problems = append(problems, tool.Name()+": required is not a []string")
			continue
		}
		for _, name := range required {
			if strings.TrimSpace(name) == "" {
				problems = append(problems, tool.Name()+": required names an empty property")
				continue
			}
			property, ok := properties[name].(map[string]any)
			if !ok {
				problems = append(problems, tool.Name()+": required "+name+" has no property")
				continue
			}
			// A property the code fills in when it is absent must not be
			// required: the two disagree about whether the argument exists, and
			// the description is what the model reads to decide.
			if description, _ := property["description"].(string); strings.Contains(description, "Defaults to") {
				problems = append(problems, tool.Name()+": required "+name+" is documented as optional")
			}
		}
	}
	return problems
}

func TestEveryToolSchemaHasUsableRequiredKeys(t *testing.T) {
	if problems := schemaProblems(t); len(problems) > 0 {
		t.Errorf("tool schemas with a broken required list:\n%s", strings.Join(problems, "\n"))
	}
}
