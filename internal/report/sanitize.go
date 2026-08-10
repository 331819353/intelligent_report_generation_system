package report

import (
	"net/url"
	"sort"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

var allowedRichTextTags = map[string]struct{}{
	"a": {}, "b": {}, "blockquote": {}, "br": {}, "code": {}, "em": {},
	"i": {}, "li": {}, "ol": {}, "p": {}, "pre": {}, "s": {}, "span": {},
	"strong": {}, "u": {}, "ul": {},
}

var droppedRichTextTags = map[string]struct{}{
	"embed": {}, "iframe": {}, "math": {}, "object": {}, "script": {}, "style": {}, "svg": {},
}

// SanitizeRichText returns a deterministic HTML fragment containing only the
// Report V1 formatting allowlist. Unsafe containers and their content are
// dropped; unknown formatting tags are unwrapped so their text remains.
func SanitizeRichText(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if !strings.ContainsAny(value, "<&") {
		return value
	}
	context := &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div}
	nodes, err := html.ParseFragment(strings.NewReader(value), context)
	if err != nil {
		return html.EscapeString(value)
	}
	var output strings.Builder
	for _, node := range nodes {
		for _, sanitized := range sanitizeRichTextNode(node) {
			_ = html.Render(&output, sanitized)
		}
	}
	return strings.TrimSpace(output.String())
}

func sanitizeRichTextNode(node *html.Node) []*html.Node {
	switch node.Type {
	case html.TextNode:
		return []*html.Node{{Type: html.TextNode, Data: node.Data}}
	case html.ElementNode:
		tag := strings.ToLower(node.Data)
		if _, drop := droppedRichTextTags[tag]; drop {
			return nil
		}
		children := sanitizeRichTextChildren(node)
		if _, allowed := allowedRichTextTags[tag]; !allowed {
			return children
		}
		clean := &html.Node{Type: html.ElementNode, Data: tag, DataAtom: atom.Lookup([]byte(tag))}
		clean.Attr = sanitizeRichTextAttributes(tag, node.Attr)
		for _, child := range children {
			clean.AppendChild(child)
		}
		return []*html.Node{clean}
	default:
		return nil
	}
}

func sanitizeRichTextChildren(node *html.Node) []*html.Node {
	result := []*html.Node{}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		result = append(result, sanitizeRichTextNode(child)...)
	}
	return result
}

func sanitizeRichTextAttributes(tag string, attributes []html.Attribute) []html.Attribute {
	if tag != "a" {
		return nil
	}
	values := map[string]string{}
	for _, attribute := range attributes {
		key := strings.ToLower(attribute.Key)
		value := strings.TrimSpace(attribute.Val)
		switch key {
		case "href":
			if safeRichTextURL(value) {
				values[key] = value
			}
		case "title":
			if value != "" {
				values[key] = value
			}
		case "target":
			if value == "_blank" || value == "_self" {
				values[key] = value
			}
		}
	}
	if values["target"] == "_blank" {
		values["rel"] = "noopener noreferrer"
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]html.Attribute, 0, len(keys))
	for _, key := range keys {
		result = append(result, html.Attribute{Key: key, Val: values[key]})
	}
	return result
}

func safeRichTextURL(value string) bool {
	if value == "" || strings.HasPrefix(value, "//") {
		return false
	}
	if strings.HasPrefix(value, "#") || strings.HasPrefix(value, "/") {
		return true
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "mailto":
		return true
	default:
		return false
	}
}
