package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

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
