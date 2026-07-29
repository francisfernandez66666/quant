package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	"os"
	"time"
)

var adj = []string{"Fast", "Sharp", "Bold", "Cool", "Top", "Big", "Hot", "Red", "Blue", "Gold", "Dark", "Safe", "Real", "High", "Low"}
var noun = []string{"Quant", "Trade", "Bull", "Bear", "Wave", "Edge", "Alpha", "Beta", "Risk", "Fund", "Vest", "Pulse", "Spark", "Storm", "Maker"}

func randInt(n int) int {
	v, _ := rand.Int(rand.Reader, big.NewInt(int64(n)))
	return int(v.Int64())
}

func randStr(n int) string {
	const chars = "abcdefghkmnpqrstuvwxyz23456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = chars[randInt(len(chars))]
	}
	return string(b)
}

type accountJSON struct {
	Username  string `json:"username"`
	Password  string `json:"password"`
	ExpiresAt int64  `json:"expires_at"`
}

func main() {
	count := flag.Int("n", 1, "生成账号数量")
	days := flag.Int("days", 14, "账号过期天数")
	appendFile := flag.String("append", "", "追加到指定 JSON 文件（如 config/rules.json）")
	showCmd := flag.Bool("cmd", false, "输出 curl 登录命令")
	flag.Parse()

	expiresAt := time.Now().AddDate(0, 0, *days).Unix()

	var accounts []accountJSON

	for i := 0; i < *count; i++ {
		u := adj[randInt(len(adj))] + noun[randInt(len(noun))] + randStr(3)
		p := randStr(4) + "@" + randStr(4)

		h := sha256.Sum256([]byte(p))
		hash := hex.EncodeToString(h[:])

		acct := accountJSON{
			Username:  u,
			Password:  hash,
			ExpiresAt: expiresAt,
		}
		accounts = append(accounts, acct)

		fmt.Printf("═══════════════════════════════════════\n")
		fmt.Printf("  #%d  量仔 测试账号\n", i+1)
		fmt.Printf("  用户名: %s\n", u)
		fmt.Printf("  密  码: %s\n", p)
		fmt.Printf("  过期日: %s\n", time.Unix(expiresAt, 0).Format("2006-01-02 15:04"))
		fmt.Println()

		if *showCmd {
			fmt.Printf("  登录命令:\n")
			fmt.Printf("  curl -X POST http://127.0.0.1:8080/api/auth/login \\\n")
			fmt.Printf("    -H 'Content-Type: application/json' \\\n")
			fmt.Printf("    -d '{\"username\":\"%s\",\"password\":\"%s\"}'\n", u, p)
			fmt.Println()
		}
		fmt.Println()
	}

	// 输出 JSON 块供导入
	enc := json.NewEncoder(os.Stderr)
	enc.SetIndent("", "  ")
	jsonData, _ := json.MarshalIndent(accounts, "", "  ")
	fmt.Fprintf(os.Stderr, "JSON accounts:\n%s\n", jsonData)

	// 追加到 rules.json
	if *appendFile != "" {
		data, err := os.ReadFile(*appendFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "读取 %s 失败: %v\n", *appendFile, err)
			os.Exit(1)
		}
		var raw interface{}
		if err := json.Unmarshal(data, &raw); err != nil {
			fmt.Fprintf(os.Stderr, "解析 %s 失败: %v\n", *appendFile, err)
			os.Exit(1)
		}
		m, ok := raw.(map[string]interface{})
		if !ok {
			fmt.Fprintf(os.Stderr, "%s 格式错误\n", *appendFile)
			os.Exit(1)
		}
		existing, _ := m["accounts"].([]interface{})
		for _, a := range accounts {
			existing = append(existing, map[string]interface{}{
				"username":   a.Username,
				"password":   a.Password,
				"expires_at": a.ExpiresAt,
			})
		}
		m["accounts"] = existing
		output, err := json.MarshalIndent(raw, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "序列化失败: %v\n", err)
			os.Exit(1)
		}
		if err := os.WriteFile(*appendFile, output, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "写入 %s 失败: %v\n", *appendFile, err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "✅ 已追加 %d 个账号到 %s\n", len(accounts), *appendFile)
	}
}
