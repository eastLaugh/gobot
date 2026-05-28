package onebot

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

func LoginQQ(conn *websocket.Conn) (int64, error) {
	echo := fmt.Sprintf("login-%d", time.Now().UnixNano())
	if err := conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return 0, err
	}
	if err := conn.WriteJSON(map[string]any{
		"action": "get_login_info",
		"params": map[string]any{},
		"echo":   echo,
	}); err != nil {
		return 0, err
	}
	if err := conn.SetWriteDeadline(time.Time{}); err != nil {
		return 0, err
	}
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return 0, err
	}
	defer conn.SetReadDeadline(time.Time{})
	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			return 0, err
		}
		var resp struct {
			Status  string `json:"status"`
			Retcode int    `json:"retcode"`
			Message string `json:"message"`
			Echo    string `json:"echo"`
			Data    struct {
				UserID int64 `json:"user_id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(payload, &resp); err != nil || resp.Echo != echo {
			continue
		}
		if resp.Status != "ok" || resp.Retcode != 0 {
			return 0, fmt.Errorf("OneBot get_login_info failed: status=%s retcode=%d message=%s", resp.Status, resp.Retcode, resp.Message)
		}
		if resp.Data.UserID == 0 {
			return 0, fmt.Errorf("OneBot get_login_info missing user_id")
		}
		return resp.Data.UserID, nil
	}
}

func SendGroupMsg(conn *websocket.Conn, groupID int64, message string) error {
	payload := map[string]any{
		"action": "send_group_msg",
		"params": map[string]any{"group_id": groupID, "message": message},
		"echo":   fmt.Sprintf("bot-%d", time.Now().UnixNano()),
	}
	if err := conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	if err := conn.WriteJSON(payload); err != nil {
		return err
	}
	return conn.SetWriteDeadline(time.Time{})
}

// SendPrivateMsg 发送私聊。groupID>0 时走群临时会话；groupID=0 时走普通私聊。
func SendPrivateMsg(conn *websocket.Conn, userID, groupID int64, message string) error {
	params := map[string]any{"user_id": userID, "message": message}
	if groupID > 0 {
		params["group_id"] = groupID
	}
	payload := map[string]any{
		"action": "send_private_msg",
		"params": params,
		"echo":   fmt.Sprintf("private-%d", time.Now().UnixNano()),
	}
	if err := conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	if err := conn.WriteJSON(payload); err != nil {
		return err
	}
	return conn.SetWriteDeadline(time.Time{})
}

func PokeGroupMember(conn *websocket.Conn, groupID, userID int64) error {
	payload := map[string]any{
		"action": "group_poke",
		"params": map[string]any{"group_id": groupID, "user_id": userID},
		"echo":   fmt.Sprintf("poke-%d", time.Now().UnixNano()),
	}
	if err := conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	if err := conn.WriteJSON(payload); err != nil {
		return err
	}
	return conn.SetWriteDeadline(time.Time{})
}

type member struct {
	UserID   int64  `json:"user_id"`
	Nickname string `json:"nickname"`
	Card     string `json:"card"`
	Role     string `json:"role"`
	Title    string `json:"title"`
}

func QueryGroupMembers(ctx context.Context, call func(context.Context, string, map[string]any) ([]byte, error), groupID int64, query string, limit int) (string, error) {
	b, err := call(ctx, "get_group_member_list", map[string]any{"group_id": groupID})
	if err != nil {
		return "", err
	}
	var resp struct {
		Status  string   `json:"status"`
		Retcode int      `json:"retcode"`
		Message string   `json:"message"`
		Data    []member `json:"data"`
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		return "", err
	}
	if resp.Status != "ok" || resp.Retcode != 0 {
		return "", fmt.Errorf("OneBot get_group_member_list failed: status=%s retcode=%d message=%s", resp.Status, resp.Retcode, resp.Message)
	}
	if limit <= 0 {
		limit = 50
	}
	query = strings.ToLower(query)
	var members []member
	for _, m := range resp.Data {
		if query != "" && !strings.Contains(strings.ToLower(fmt.Sprint(m.UserID)), query) && !strings.Contains(strings.ToLower(m.Nickname), query) && !strings.Contains(strings.ToLower(m.Card), query) {
			continue
		}
		members = append(members, m)
	}
	slices.SortFunc(members, func(a, b member) int {
		return strings.Compare(displayName(a), displayName(b))
	})
	if len(members) == 0 {
		return "没有匹配的群成员。", nil
	}

	var out strings.Builder
	fmt.Fprintf(&out, "当前群匹配到 %d 个成员", len(members))
	if len(members) > limit {
		fmt.Fprintf(&out, "，只显示前 %d 个", limit)
	}
	out.WriteString("：\n")
	for i, m := range members {
		if i >= limit {
			break
		}
		fmt.Fprintf(&out, "- %d | %s", m.UserID, displayName(m))
		if m.Role != "" {
			fmt.Fprintf(&out, " | %s", m.Role)
		}
		if m.Title != "" {
			fmt.Fprintf(&out, " | %s", m.Title)
		}
		out.WriteString("\n")
	}
	return strings.TrimRight(out.String(), "\n"), nil
}

func displayName(m member) string {
	if m.Card != "" {
		return m.Card
	}
	if m.Nickname != "" {
		return m.Nickname
	}
	return fmt.Sprint(m.UserID)
}
