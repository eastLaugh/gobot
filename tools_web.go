package main

import (
	"context"
	"encoding/xml"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var webSearchHTTP = &http.Client{Timeout: 12 * time.Second}

func WebSearch(ctx context.Context, args *struct {
	Query string `description:"要搜索的关键词。"`
}) string {
	q := strings.TrimSpace(args.Query)
	if q == "" {
		return "搜索失败：关键词为空"
	}

	u, _ := url.Parse("https://www.bing.com/search")
	v := u.Query()
	v.Set("format", "rss")
	v.Set("q", q)
	u.RawQuery = v.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return fmt.Sprintf("搜索失败：%v", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")

	resp, err := webSearchHTTP.Do(req)
	if err != nil {
		return fmt.Sprintf("搜索失败：%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Sprintf("搜索失败：HTTP %s", resp.Status)
	}

	var rss struct {
		Channel struct {
			Items []struct {
				Title       string `xml:"title"`
				Link        string `xml:"link"`
				Description string `xml:"description"`
			} `xml:"item"`
		} `xml:"channel"`
	}
	if err := xml.NewDecoder(resp.Body).Decode(&rss); err != nil {
		return fmt.Sprintf("搜索失败：%v", err)
	}
	if len(rss.Channel.Items) == 0 {
		return "没有搜索结果"
	}

	var b strings.Builder
	for i, item := range rss.Channel.Items {
		if i >= 5 {
			break
		}
		fmt.Fprintf(&b, "%d. %s\n%s\n%s\n", i+1, cleanSearchText(item.Title), item.Link, cleanSearchText(item.Description))
	}
	return strings.TrimSpace(b.String())
}

func cleanSearchText(s string) string {
	s = html.UnescapeString(s)
	s = strings.Join(strings.Fields(s), " ")
	return s
}
