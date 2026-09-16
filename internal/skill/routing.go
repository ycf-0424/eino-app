package skill

// 工具名按实际注册名精确匹配；不把技能名称或近似名称当作可执行能力。
func missingTools(required, available []string) []string {
	known := make(map[string]bool, len(available))
	for _, name := range available {
		known[name] = true
	}
	var missing []string
	for _, name := range required {
		if !known[name] {
			missing = append(missing, name)
		}
	}
	return missing
}
