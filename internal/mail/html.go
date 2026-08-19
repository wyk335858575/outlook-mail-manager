package mail

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

var messageHTMLPolicy = newMessageHTMLPolicy()

func newMessageHTMLPolicy() *bluemonday.Policy {
	policy := bluemonday.UGCPolicy()
	policy.AllowElements("font", "main", "header", "footer")
	policy.AllowAttrs("border").Matching(bluemonday.Integer).OnElements("img", "table")
	policy.AllowAttrs("cellpadding", "cellspacing").Matching(bluemonday.Integer).OnElements("table")
	policy.AllowDataURIImages()
	policy.RequireNoFollowOnLinks(true)
	policy.RequireNoReferrerOnLinks(true)
	return policy
}

func sanitizeMessageHTML(raw string, loadRemoteImages bool) (string, error) {
	sanitized := messageHTMLPolicy.Sanitize(raw)
	contextNode := &xhtml.Node{Type: xhtml.ElementNode, Data: "div", DataAtom: atom.Div}
	nodes, err := xhtml.ParseFragment(strings.NewReader(sanitized), contextNode)
	if err != nil {
		return "", fmt.Errorf("parse sanitized message HTML: %w", err)
	}
	for _, node := range nodes {
		filterMessageContent(node, loadRemoteImages)
	}

	var fragment strings.Builder
	for _, node := range nodes {
		if err := xhtml.Render(&fragment, node); err != nil {
			return "", fmt.Errorf("render sanitized message HTML: %w", err)
		}
	}
	return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><style>
html{color-scheme:light}body{margin:0;padding:18px;color:#263640;background:#fff;font:14px/1.65 "Microsoft YaHei UI","Segoe UI",sans-serif;overflow-wrap:anywhere}img{max-width:100%;height:auto}table{max-width:100%}pre{white-space:pre-wrap}blockquote{margin-left:0;padding-left:14px;border-left:3px solid #cbd5dc}a{color:#176a9b}
</style></head><body>` + fragment.String() + `</body></html>`, nil
}

func filterMessageContent(node *xhtml.Node, loadRemoteImages bool) {
	if node.Type == xhtml.ElementNode && (node.DataAtom == atom.Img || node.DataAtom == atom.A) {
		attributes := node.Attr[:0]
		for _, attribute := range node.Attr {
			if strings.EqualFold(attribute.Key, "src") && !allowedMessageImageURL(attribute.Val, loadRemoteImages) {
				continue
			}
			if node.DataAtom == atom.A && strings.EqualFold(attribute.Key, "href") {
				continue
			}
			attributes = append(attributes, attribute)
		}
		node.Attr = attributes
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		filterMessageContent(child, loadRemoteImages)
	}
}

func allowedMessageImageURL(value string, loadRemoteImages bool) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return false
	}
	if strings.EqualFold(parsed.Scheme, "data") {
		return true
	}
	return loadRemoteImages && strings.EqualFold(parsed.Scheme, "https") && parsed.Host != ""
}
