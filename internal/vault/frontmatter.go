package vault

import (
	"strings"

	"gopkg.in/yaml.v3"
)

type NoteContent struct {
	Frontmatter    map[string]any
	Body           string
	HasFrontmatter bool
}

func ParseFrontmatter(data []byte) NoteContent {
	text := string(data)
	if !strings.HasPrefix(text, "---") {
		return NoteContent{Body: text}
	}
	rest := text[3:]
	if rest != "" && rest[0] != '\n' && rest[0] != '\r' {
		return NoteContent{Body: text}
	}
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return NoteContent{Body: text}
	}
	fmText := rest[:end]
	body := rest[end+4:]
	body = strings.TrimLeft(body, "\r\n")
	var fm map[string]any
	if err := yaml.Unmarshal([]byte(fmText), &fm); err != nil {
		return NoteContent{Body: text}
	}
	return NoteContent{
		Frontmatter:    fm,
		Body:           body,
		HasFrontmatter: true,
	}
}
