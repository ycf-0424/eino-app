package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"gopkg.in/yaml.v3"
)

// OpenAI 兼容模型配置。
type OpenAI struct {
	APIKey              string `yaml:"api_key"`
	Model               string `yaml:"model"`
	BaseURL             string `yaml:"base_url"`
	ReasoningEffort     string `yaml:"reasoning_effort"`
	MaxCompletionTokens int    `yaml:"max_completion_tokens"`
}

// Config 顶层配置结构。
type Config struct {
	MySQL  MySQL  `yaml:"mysql"`
	OpenAI OpenAI `yaml:"openai"`
	// SessionDir 是会话 JSON 文件的保存目录。
	SessionDir string `yaml:"session_dir"`
	// Debug 控制是否启用 Eino Callback 和工具审计日志。
	Debug bool `yaml:"debug"`
	// Retry 定义所有模型调用共用的重试策略。
	Retry   Retry   `yaml:"retry"`
	RAG     RAG     `yaml:"rag"`
	Memory  Memory  `yaml:"memory"`
	Agent   Agent   `yaml:"agent"`
	Ollama  Ollama  `yaml:"ollama"`
	Session Session `yaml:"session"`
	Skills  Skills  `yaml:"skills"`
	Runtime Runtime `yaml:"runtime"`
	// LocalFiles 控制 Agent 能读取哪些本地目录；未列出的路径一律拒绝。
	LocalFiles LocalFiles `yaml:"local_files"`
	// ExecutionEvents 控制实时执行进度与执行记录；默认关闭，关闭时行为与 P7 一致。
	ExecutionEvents ExecutionEvents `yaml:"execution_events"`
	// Auth 控制登录认证与多用户隔离；关闭时以单用户模式运行，行为与改造前一致。
	Auth Auth `yaml:"auth"`
	// ProjectDir 是实际找到的 config.yaml 所在目录，不参与 YAML 序列化。
	ProjectDir string `yaml:"-"`
}

// ExecutionEvents 是 P8 实时执行进度的灰度开关与规模参数。
type ExecutionEvents struct {
	// Enabled 关闭时完全不采集、不传输、不落库，行为与 P7 完全一致。
	Enabled bool `yaml:"enabled"`
	// QueueSize 是 WebSocket 出站队列容量，队列满时中间事件可丢、终态必须送达。
	QueueSize int `yaml:"queue_size"`
	// RetentionDays 是执行记录的保留天数。
	RetentionDays int `yaml:"retention_days"`
	// MaxEventsPerRun 限制单个 run 落库的事件数量。
	MaxEventsPerRun int `yaml:"max_events_per_run"`
	// Dir 是文件后端的执行日志目录。
	Dir string `yaml:"dir"`
}

// LocalFiles 是本地文件只读工具的安全边界。
type LocalFiles struct {
	Enabled  bool     `yaml:"enabled"`
	Roots    []string `yaml:"roots"`
	MaxBytes int64    `yaml:"max_bytes"`
}

// Runtime 控制本地模型的请求超时和全局并发上限。
type Runtime struct {
	RequestTimeout Duration `yaml:"request_timeout"`
	MaxConcurrency int      `yaml:"max_concurrency"`
}

// Skills 类型。
type Skills struct {
	Dir     string `yaml:"dir"`
	Default string `yaml:"default"`
}

// Session 控制历史长度，避免长对话拖垮本地模型上下文。
type Session struct {
	Store       string `yaml:"store"` // file 或 mysql；默认 file 兼容已有部署。
	MaxMessages int    `yaml:"max_messages"`
	MaxChars    int    `yaml:"max_chars"`
	ExpireDays  int    `yaml:"expire_days"`
}

// MySQL 连接现有实例；密码通过 MYSQL_PASSWORD 环境变量覆盖。
type MySQL struct {
	Host     string `yaml:"host"`
	Port     string `yaml:"port"`
	Database string `yaml:"database"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
}

// Ollama 控制本地模型健康检查和请求超时。
type Ollama struct {
	HealthCheck bool     `yaml:"health_check"`
	Timeout     Duration `yaml:"timeout"`
}

// Agent 类型。
type Agent struct {
	MultiAgent    bool   `yaml:"multi_agent"`
	CheckpointDir string `yaml:"checkpoint_dir"`
}

// RAG 配置知识库的 Embedding、向量存储与检索参数。
type RAG struct {
	Enabled         bool    `yaml:"enabled"`
	Store           string  `yaml:"store"`
	DocumentDir     string  `yaml:"document_dir"`
	ChunkSize       int     `yaml:"chunk_size"`
	ChunkOverlap    int     `yaml:"chunk_overlap"`
	TopK            int     `yaml:"top_k"`
	ScoreThreshold  float64 `yaml:"score_threshold"`
	MaxContextChars int     `yaml:"max_context_chars"`
	Embedding       OpenAI  `yaml:"embedding"`
	Dimension       int     `yaml:"dimension"`
	Redis           Redis   `yaml:"redis"`
	Milvus          Milvus  `yaml:"milvus"`
}

// Redis 类型。
type Redis struct {
	Addr      string `yaml:"addr"`
	Password  string `yaml:"password"`
	DB        int    `yaml:"db"`
	Index     string `yaml:"index"`
	KeyPrefix string `yaml:"key_prefix"`
}

// Milvus 描述 Milvus Standalone/Cluster 的连接与 Collection。
type Milvus struct {
	Address    string `yaml:"address"`
	Username   string `yaml:"username"`
	Password   string `yaml:"password"`
	Collection string `yaml:"collection"`
}

// Retry 控制模型请求失败后的指数退避策略。
type Retry struct {
	// MaxAttempts 包含第一次调用，例如 3 表示初次调用加两次重试。
	MaxAttempts int `yaml:"max_attempts"`
	// BaseDelay 是第一次重试前的基础等待时间。
	BaseDelay Duration `yaml:"base_delay"`
	// MaxDelay 限制指数退避的最长基础等待时间。
	MaxDelay Duration `yaml:"max_delay"`
}

// Duration 让 YAML 可以使用 500ms、1s 这类易读时长。
type Duration time.Duration

// UnmarshalYAML 把 YAML 字符串转换为 time.Duration。
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	v, err := time.ParseDuration(node.Value)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", node.Value, err)
	}
	*d = Duration(v)
	return nil
}

// Validate 检查配置中必须存在的字段。
func (c *Config) Validate() error {
	if value := os.Getenv("SESSION_STORE"); value != "" {
		c.Session.Store = value
	}
	if c.Session.Store == "" {
		c.Session.Store = "file"
	}
	if c.Session.Store != "file" && c.Session.Store != "mysql" {
		return fmt.Errorf("session.store must be file or mysql")
	}
	for key, dest := range map[string]*string{"MYSQL_HOST": &c.MySQL.Host, "MYSQL_PORT": &c.MySQL.Port, "MYSQL_DATABASE": &c.MySQL.Database, "MYSQL_USER": &c.MySQL.User, "MYSQL_PASSWORD": &c.MySQL.Password} {
		if value, ok := os.LookupEnv(key); ok {
			*dest = value
		}
	}
	if c.MySQL.Host == "" {
		c.MySQL.Host = "localhost"
	}
	if c.MySQL.Port == "" {
		c.MySQL.Port = "3306"
	}
	if c.MySQL.Database == "" {
		c.MySQL.Database = "eino"
	}
	if c.Session.Store == "mysql" && c.MySQL.User == "" {
		return fmt.Errorf("MYSQL_USER is required for mysql session store")
	}
	// 配置值支持 ${ENV_NAME}，未设置的变量会立即报错而不是发送空密钥。
	var err error
	for name, value := range map[string]*string{"openai.api_key": &c.OpenAI.APIKey, "openai.model": &c.OpenAI.Model, "openai.base_url": &c.OpenAI.BaseURL, "rag.embedding.api_key": &c.RAG.Embedding.APIKey, "rag.embedding.model": &c.RAG.Embedding.Model, "rag.embedding.base_url": &c.RAG.Embedding.BaseURL, "rag.redis.password": &c.RAG.Redis.Password, "rag.milvus.password": &c.RAG.Milvus.Password} {
		*value, err = expandEnvironment(*value)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	if strings.TrimSpace(c.OpenAI.Model) == "" {
		return fmt.Errorf("model is required")
	}

	if strings.TrimSpace(c.OpenAI.BaseURL) == "" {
		return fmt.Errorf("base_url is required")
	}
	// Ollama 的思考型模型可使用 none 关闭推理，以缩短简单任务的响应时间。
	switch c.OpenAI.ReasoningEffort {
	case "", "none", "low", "medium", "high":
	default:
		return fmt.Errorf("openai.reasoning_effort must be none, low, medium, or high")
	}
	if c.OpenAI.MaxCompletionTokens < 0 {
		return fmt.Errorf("openai.max_completion_tokens must not be negative")
	}

	// 本地 Ollama 通常不校验 API Key。
	// 远程 OpenAI 兼容服务则要求配置 API Key。
	baseURL := strings.ToLower(c.OpenAI.BaseURL)
	isLocal := strings.Contains(baseURL, "localhost") ||
		strings.Contains(baseURL, "127.0.0.1")

	if !isLocal && strings.TrimSpace(c.OpenAI.APIKey) == "" {
		return fmt.Errorf("api_key is required for remote provider")
	}
	if strings.TrimSpace(c.SessionDir) == "" {
		// 允许旧配置不写 session_dir，并提供可直接运行的默认值。
		c.SessionDir = "data/sessions"
	}
	// retry 段可以省略；零值在这里补成安全默认值。
	if c.Retry.MaxAttempts == 0 {
		c.Retry.MaxAttempts = 3
	}
	if c.Retry.MaxAttempts < 1 {
		return fmt.Errorf("retry.max_attempts must be greater than zero")
	}
	if c.Retry.BaseDelay == 0 {
		c.Retry.BaseDelay = Duration(time.Second)
	}
	if c.Retry.MaxDelay == 0 {
		c.Retry.MaxDelay = Duration(10 * time.Second)
	}
	if c.Retry.BaseDelay < 0 || c.Retry.MaxDelay < c.Retry.BaseDelay {
		return fmt.Errorf("retry delay configuration is invalid")
	}
	if c.Agent.CheckpointDir == "" {
		c.Agent.CheckpointDir = "data/checkpoints"
	}
	if c.Ollama.Timeout == 0 {
		c.Ollama.Timeout = Duration(10 * time.Second)
	}
	if c.Skills.Dir == "" {
		c.Skills.Dir = "skills"
	}
	if c.Runtime.RequestTimeout == 0 {
		c.Runtime.RequestTimeout = Duration(2 * time.Minute)
	}
	if c.Runtime.MaxConcurrency == 0 {
		c.Runtime.MaxConcurrency = 2
	}
	if c.Runtime.MaxConcurrency < 1 {
		return fmt.Errorf("runtime.max_concurrency must be greater than zero")
	}
	if c.LocalFiles.Enabled {
		if len(c.LocalFiles.Roots) == 0 {
			c.LocalFiles.Roots = []string{"workspace-files"}
		}
		if c.LocalFiles.MaxBytes == 0 {
			c.LocalFiles.MaxBytes = 10 << 20
		}
		if c.LocalFiles.MaxBytes < 1 {
			return fmt.Errorf("local_files.max_bytes must be greater than zero")
		}
		for _, root := range c.LocalFiles.Roots {
			if strings.TrimSpace(root) == "" {
				return fmt.Errorf("local_files.roots must not contain empty paths")
			}
		}
	}
	if c.ExecutionEvents.Enabled {
		// 只在开启时补默认值，关闭时保持零值，便于判断「未启用」。
		if c.ExecutionEvents.QueueSize <= 0 {
			c.ExecutionEvents.QueueSize = 256
		}
		if c.ExecutionEvents.RetentionDays <= 0 {
			c.ExecutionEvents.RetentionDays = 7
		}
		if c.ExecutionEvents.MaxEventsPerRun <= 0 {
			c.ExecutionEvents.MaxEventsPerRun = 5000
		}
		if strings.TrimSpace(c.ExecutionEvents.Dir) == "" {
			c.ExecutionEvents.Dir = "data/executions"
		}
	}
	if c.RAG.Enabled {
		if c.RAG.Store == "" {
			c.RAG.Store = "redis"
		}
		if c.RAG.Store != "redis" && c.RAG.Store != "milvus" {
			return fmt.Errorf("rag.store must be redis or milvus")
		}
		if c.RAG.Embedding.Model == "" || c.RAG.Embedding.BaseURL == "" {
			return fmt.Errorf("rag.embedding model and base_url are required")
		}
		if c.RAG.Dimension <= 0 {
			return fmt.Errorf("rag.dimension must be greater than zero")
		}
		if c.RAG.DocumentDir == "" {
			c.RAG.DocumentDir = "docs/knowledge"
		}
		if c.RAG.ChunkSize == 0 {
			c.RAG.ChunkSize = 800
		}
		if c.RAG.ChunkOverlap == 0 {
			c.RAG.ChunkOverlap = 120
		}
		if c.RAG.TopK == 0 {
			c.RAG.TopK = 5
		}
		if c.RAG.ChunkOverlap >= c.RAG.ChunkSize {
			return fmt.Errorf("rag.chunk_overlap must be smaller than chunk_size")
		}
		if c.RAG.Redis.Addr == "" {
			c.RAG.Redis.Addr = "localhost:6379"
		}
		if c.RAG.Redis.Index == "" {
			c.RAG.Redis.Index = "my_eino_knowledge"
		}
		if c.RAG.Redis.KeyPrefix == "" {
			c.RAG.Redis.KeyPrefix = "knowledge:"
		}
		if c.RAG.Milvus.Address == "" {
			c.RAG.Milvus.Address = "localhost:19530"
		}
		if c.RAG.Milvus.Collection == "" {
			c.RAG.Milvus.Collection = "my_eino_knowledge"
		}
	}

	if err := c.ValidateAuth(); err != nil {
		return err
	}
	return c.ValidateMemory()
}

func expandEnvironment(value string) (string, error) {
	var missing string
	expanded := os.Expand(value, func(key string) string {
		v, ok := os.LookupEnv(key)
		if !ok {
			missing = key
		}
		return v
	})
	if missing != "" {
		return "", fmt.Errorf("environment variable %s is not set", missing)
	}
	return expanded, nil
}

// Load 加载项目根目录的 config.yaml。
func Load() (*Config, error) {
	var lastErr error

	for _, path := range []string{
		"config.yaml",
		"../config.yaml",
		"../../config.yaml",
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			lastErr = err
			continue
		}

		// 本机启动也读取项目 .env；已设置的进程环境变量优先，不被文件覆盖。
		if envErr := godotenv.Load(filepath.Join(filepath.Dir(path), ".env")); envErr != nil && !os.IsNotExist(envErr) {
			return nil, fmt.Errorf("load .env: %w", envErr)
		}
		cfg := &Config{}
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}

		if err := cfg.Validate(); err != nil {
			return nil, fmt.Errorf("config: %w", err)
		}
		absoluteConfig, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("resolve config path: %w", err)
		}
		cfg.ProjectDir = filepath.Dir(absoluteConfig)
		cfg.SessionDir = resolveProjectPath(cfg.ProjectDir, cfg.SessionDir)
		cfg.Agent.CheckpointDir = resolveProjectPath(cfg.ProjectDir, cfg.Agent.CheckpointDir)
		cfg.RAG.DocumentDir = resolveProjectPath(cfg.ProjectDir, cfg.RAG.DocumentDir)
		cfg.Skills.Dir = resolveProjectPath(cfg.ProjectDir, cfg.Skills.Dir)
		if cfg.ExecutionEvents.Dir != "" {
			cfg.ExecutionEvents.Dir = resolveProjectPath(cfg.ProjectDir, cfg.ExecutionEvents.Dir)
		}
		for i, root := range cfg.LocalFiles.Roots {
			cfg.LocalFiles.Roots[i] = resolveProjectPath(cfg.ProjectDir, root)
		}

		return cfg, nil
	}

	return nil, fmt.Errorf(
		"config.yaml not found (config dir up from cwd): %w",
		lastErr,
	)
}

func resolveProjectPath(root, value string) string {
	if value == "" || filepath.IsAbs(value) {
		return value
	}
	return filepath.Join(root, value)
}
