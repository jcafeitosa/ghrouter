package cli

import "charm.land/bubbles/v2/key"

type liveKeyMap struct {
	Navigate key.Binding
	Sections key.Binding
	Refresh  key.Binding
	Filter   key.Binding
	Palette  key.Binding
	Help     key.Binding
	Quit     key.Binding
}

func (k liveKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Navigate, k.Sections, k.Refresh, k.Palette, k.Help, k.Quit}
}

func (k liveKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Navigate, k.Sections, k.Refresh, k.Filter},
		{k.Palette, k.Help, k.Quit},
	}
}

func dashboardKeyMap() liveKeyMap {
	return liveKeyMap{
		Navigate: key.NewBinding(key.WithKeys("↑/k", "↓/j"), key.WithHelp("↑/↓", "navigate")),
		Sections: key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "sections")),
		Refresh:  key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
		Filter:   key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "filter")),
		Palette:  key.NewBinding(key.WithKeys("/", "ctrl+p"), key.WithHelp("/", "commands")),
		Help:     key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:     key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
	}
}
