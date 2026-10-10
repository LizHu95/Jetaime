package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/LizHu95/Jetaime/services/api/internal/decisions"
	"github.com/LizHu95/Jetaime/services/api/internal/demo"
)

// loadFixture 每次调用都从磁盘读取，拒绝未知字段和多个 JSON 对象。
// 实体校验由 MemoryStore 构造负责；读取失败时不会修改运行中的资料。
func loadFixture(path string) (demo.Fixture, error) {
	file, err := os.Open(path)
	if err != nil {
		return demo.Fixture{}, fmt.Errorf("读取资料 %s：%w", path, err)
	}
	defer func() { _ = file.Close() }()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var fixture demo.Fixture
	if err := decoder.Decode(&fixture); err != nil {
		return demo.Fixture{}, fmt.Errorf("解析资料 %s：%w", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return demo.Fixture{}, fmt.Errorf("资料文件必须只包含一个 JSON 对象")
	}
	return fixture, nil
}

// newFixtureService 将语义相同的 JSON 资料映射为相同版本；仅改排版不改变 SHA-256。
// 每次 reload 重新计算，Trace 能区分修改前后的数据，版本不包含文件路径或正文。
func newFixtureService(store *decisions.MemoryStore, fixture demo.Fixture, provider decisions.Provider) (*decisions.DecisionService, error) {
	encoded, err := json.Marshal(fixture)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(encoded)
	return decisions.NewDecisionService(store, demo.Checker{Facts: fixture.Facts}, provider, decisions.ServiceConfig{DataVersion: hex.EncodeToString(hash[:])})
}
