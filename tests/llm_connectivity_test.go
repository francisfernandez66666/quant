// Package tests — LLM 连通性测试
// 运行: go test ./tests -run TestLLM_Connectivity -v -count=1 -timeout 120s
package tests

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"quant-trading/internal/llm"
)

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestLLM_Connectivity(t *testing.T) {
	data, err := os.ReadFile("../config/secrets.json")
	if err != nil {
		t.Fatalf("无法读取 config/secrets.json: %v", err)
	}
	var sec struct {
		LlmToken string `json:"llm_token"`
	}
	if err := json.Unmarshal(data, &sec); err != nil {
		t.Fatalf("解析 secrets.json 失败: %v", err)
	}
	if sec.LlmToken == "" {
		t.Fatalf("secrets.json 中 llm_token 为空")
	}

	apiKey := sec.LlmToken
	fmt.Printf("llm_token: %s...\n", apiKey[:minInt(len(apiKey), 16)])

	if envKey := os.Getenv("LLM_API_KEY"); envKey != "" {
		apiKey = envKey
		fmt.Printf("使用环境变量 LLM_API_KEY: %s...\n", apiKey[:minInt(len(apiKey), 16)])
	}

	client := llm.New(apiKey, "Qwen/Qwen3-8B")
	fmt.Printf("API URL: https://api.siliconflow.cn/v1/chat/completions\n")
	fmt.Printf("Model: Qwen/Qwen3-8B\n")

	title := "特斯拉宣布量产optimus机器人，人形机器人产业链受益"

	fmt.Println("\n=== 测试1: AnalyzeHotTopic ===")
	ht, err := client.AnalyzeHotTopic(title)
	if err != nil {
		t.Logf("AnalyzeHotTopic ERROR: %v", err)
	} else {
		j, _ := json.MarshalIndent(ht, "", "  ")
		fmt.Printf("结果:\n%s\n", string(j))
		if len(ht.Sectors) == 0 {
			t.Log("⚠ 板块为空 — 可能走到了关键词兜底")
		}
		if ht.Score < 0.3 {
			t.Log("⚠ 评分偏低")
		}
	}

	fmt.Println("\n=== 测试2: Chat (裸 API) ===")
	resp, err := client.Chat("你是一个A股分析师。只输出JSON。", "判断利好利空: "+title)
	if err != nil {
		t.Logf("Chat ERROR: %v", err)
	} else {
		fmt.Printf("Chat 返回 (%d chars):\n%s\n", len(resp), resp[:minInt(len(resp), 500)])
	}

	fmt.Println("\n=== 测试3: AnalyzeSentiment ===")
	score, err := client.AnalyzeSentiment(title)
	if err != nil {
		t.Logf("AnalyzeSentiment ERROR: %v", err)
	} else {
		fmt.Printf("情感评分: %.2f\n", score)
	}

	fmt.Println("\n=== 诊断完成 ===")
}
