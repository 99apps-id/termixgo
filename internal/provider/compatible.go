package provider

import "strings"

// CompatibleID is the default (unnamed) OpenAI-compatible provider id.
const CompatibleID = "openai-compatible"

// IsCompatible reports whether id is the openai-compatible provider or one of
// its named instances (openai-compatible/<name>). A bare "openai-compatible/"
// with no name is not a real instance, so it is rejected.
func IsCompatible(id string) bool {
	if id == CompatibleID {
		return true
	}
	name, ok := strings.CutPrefix(id, CompatibleID+"/")
	return ok && name != ""
}

// CompatibleName returns the instance name, or "" for the default slot.
func CompatibleName(id string) string {
	if !strings.HasPrefix(id, CompatibleID+"/") {
		return ""
	}
	return strings.TrimPrefix(id, CompatibleID+"/")
}

// CompatibleProviderID builds the provider id for a named instance; "" is the
// default slot.
func CompatibleProviderID(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return CompatibleID
	}
	return CompatibleID + "/" + name
}

// CompatibleLabel is the display label for a named instance.
func CompatibleLabel(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "OpenAI Compatible"
	}
	return "OpenAI Compatible (" + name + ")"
}
