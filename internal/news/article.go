// SPDX-License-Identifier: GPL-3.0-or-later

package news

import (
	"html"
	"regexp"
	"strings"
	"unicode"
)

// The announcement page is plain server-rendered HTML: a titled header, a
// date line, one content block, and unrelated furniture (breadcrumbs and a
// related-article blurb) around it that must stay out of the reading panel.
var (
	titlePattern  = regexp.MustCompile(`(?is)<h1[^>]*\bclass="[^"]*articleTitle[^"]*"[^>]*>(.*?)</h1>`)
	datePattern   = regexp.MustCompile(`(?is)<p[^>]*\bclass="[^"]*articleDate[^"]*"[^>]*>\s*([0-9]{4}\.[0-9]{1,2}\.[0-9]{1,2})\s*</p>`)
	scriptPattern = regexp.MustCompile(`(?is)<script\b.*?</script\s*>`)
	stylePattern  = regexp.MustCompile(`(?is)<style\b.*?</style\s*>`)
	breakPattern  = regexp.MustCompile(`(?i)<br\s*/?>`)
	paraEnd       = regexp.MustCompile(`(?i)</p\s*>`)
	tagPattern    = regexp.MustCompile(`(?s)<[^>]*>`)
)

const paragraphMark = "\x00"

// parseArticle extracts the title, the published date and the readable
// paragraphs of one official announcement page.
func parseArticle(page []byte, url string) (Detail, error) {
	text := string(page)
	detail := Detail{URL: url}
	if match := titlePattern.FindStringSubmatch(text); match != nil {
		detail.Title = cleanText(match[1])
	}
	if match := datePattern.FindStringSubmatch(text); match != nil {
		detail.Date = match[1]
	}
	block, ok := contentBlock(text)
	if !ok {
		return Detail{}, ErrArticleText
	}
	block = scriptPattern.ReplaceAllString(block, "")
	block = stylePattern.ReplaceAllString(block, "")
	block = breakPattern.ReplaceAllString(block, " ")
	block = paraEnd.ReplaceAllString(block, paragraphMark)
	for _, chunk := range strings.Split(block, paragraphMark) {
		if paragraph := cleanText(chunk); paragraph != "" {
			detail.Paragraphs = append(detail.Paragraphs, paragraph)
		}
	}
	if len(detail.Paragraphs) == 0 {
		return Detail{}, ErrArticleText
	}
	if detail.Title == "" {
		detail.Title = detail.Paragraphs[0]
	}
	return detail, nil
}

// contentBlock returns the inner HTML of <div class="articleContent">, using
// the nesting depth of div tags so that nested markup stays inside it and the
// related-article block outside stays out.
func contentBlock(text string) (string, bool) {
	lower := strings.ToLower(text)
	marker := strings.Index(lower, `class="articlecontent"`)
	if marker < 0 {
		return "", false
	}
	body := strings.ToLower(text)
	open := strings.Index(body[marker:], ">")
	if open < 0 {
		return "", false
	}
	start := marker + open + 1
	depth := 1
	pos := start
	for {
		open := strings.Index(body[pos:], "<div")
		close := strings.Index(body[pos:], "</div")
		if close < 0 {
			return "", false
		}
		if open >= 0 && open < close {
			depth++
			pos += open + len("<div")
			continue
		}
		end := pos + close
		depth--
		if depth == 0 {
			return text[start:end], true
		}
		pos = end + len("</div")
	}
}

// cleanText drops markup, decodes entities and collapses every run of
// whitespace (including the non-breaking spaces the pages use as padding).
func cleanText(chunk string) string {
	chunk = tagPattern.ReplaceAllString(chunk, " ")
	chunk = html.UnescapeString(chunk)
	var out strings.Builder
	pending := false
	for _, r := range chunk {
		if unicode.IsSpace(r) || r == '\u200b' {
			pending = true
			continue
		}
		if pending && out.Len() > 0 {
			out.WriteByte(' ')
		}
		pending = false
		out.WriteRune(r)
	}
	return out.String()
}
