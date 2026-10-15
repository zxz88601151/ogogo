// 命令 genuser 用于生成 users.json 中的 PBKDF2 口令哈希。
// 用法：go run ./cmd/genuser <username> <password> <displayName> <role>
// 角色：teacher | admin（academic_offices 为预留角色，本版本不激活）
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"edudesk/internal/auth"
)

func main() {
	if len(os.Args) < 5 {
		fmt.Println("用法: go run ./cmd/genuser <username> <password> <displayName> <role>")
		os.Exit(1)
	}
	u := auth.User{
		Username:     os.Args[1],
		PasswordHash: auth.HashPassword(os.Args[2]),
		DisplayName:  os.Args[3],
		Role:         auth.Role(os.Args[4]),
		Active:       true,
	}
	out, _ := json.MarshalIndent(u, "", "  ")
	fmt.Println(string(out))
}