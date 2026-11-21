package agent

// AdvertisedTools is the registry a request actually carries: everything when
// tool search is off, else the always-on set followed by the session's
// discovered tools in discovery order. The context gauge uses it so an empty
// session does not report the whole catalogue's schemas as being sent.
func AdvertisedTools(registry *Registry, toolSearch bool, discovered []string) *Registry {
	if registry == nil {
		return nil
	}
	return (&Runner{Tools: registry, ToolSearch: toolSearch}).stepTools(discovered)
}
