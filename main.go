package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	cReset  = "\033[0m"
	cRed    = "\033[91m"
	cGreen  = "\033[92m"
	cYellow = "\033[93m"
	cCyan   = "\033[96m"
	cGray   = "\033[90m"
	cPurple = "\033[95m"
)

const (
	coffeeBaseURL     = "https://coffeecatai.openel.top"
	coffeeGenURLFmt   = coffeeBaseURL + "/api/image/generation/%s"
	coffeePollURL     = coffeeBaseURL + "/api/image/generation"
	coffeePromptURL   = coffeeBaseURL + "/api/image/prompt/free"
	coffeeUpscaleURL  = coffeeBaseURL + "/api/image/upscale"
	coffeeDescribeURL = coffeeBaseURL + "/api/image/describe/free"
	turnstileSiteKey  = "0x4AAAAAACJLXZu8e5k56IR-"
	turnstileSiteURL  = "https://www.coffeecatai.com"
	userAgent         = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/144.0.0.0 Safari/537.36 Edg/144.0.0.0"
	captchaTokenTTL   = 250 * time.Second
)

type timedToken struct {
	token     string
	createdAt time.Time
}
type ImageSize struct {
	Width  int
	Height int
	Ratio  string
}

var sizeMap = map[string]ImageSize{
	"1:1":  {Width: 1024, Height: 1024, Ratio: "1:1"},
	"2:3":  {Width: 832, Height: 1248, Ratio: "2:3"},
	"3:2":  {Width: 1248, Height: 832, Ratio: "3:2"},
	"3:4":  {Width: 864, Height: 1184, Ratio: "3:4"},
	"4:3":  {Width: 1184, Height: 864, Ratio: "4:3"},
	"4:5":  {Width: 896, Height: 1152, Ratio: "4:5"},
	"5:4":  {Width: 1152, Height: 896, Ratio: "5:4"},
	"9:16": {Width: 768, Height: 1344, Ratio: "9:16"},
	"16:9": {Width: 1344, Height: 768, Ratio: "16:9"},
	"21:9": {Width: 1536, Height: 672, Ratio: "21:9"},
}

var defaultSize = sizeMap["3:2"]

/* upscaleModelMap 超分模型别名 → CoffeeCat checkpoint 名称 */
var upscaleModelMap = map[string]string{
	"ultrasharp": "4x-UltraSharpV2.safetensors",
	"animesharp": "2x-AnimeSharpV4_RCAN.safetensors",
	"realesrgan": "RealESRGAN_x4plus.pth",
	"seedvr":     "seedvr",
}

var defaultUpscaleModel = "4x-UltraSharpV2.safetensors"

/*
parseUpscaleModel 从模型名中解析超分 checkpoint
支持格式：
  - "banana-upscale"            → 默认 UltraSharp V2
  - "banana-upscale-realesrgan" → RealESRGAN_x4plus.pth
  - "banana-upscale-animesharp" → AnimeSharp V4
  - "banana-upscale-seedvr"     → SeedVR2
*/
func parseUpscaleModel(model string) string {
	for alias, ckpt := range upscaleModelMap {
		if strings.HasSuffix(model, "-"+alias) {
			return ckpt
		}
	}
	return defaultUpscaleModel
}

/*
OpenAIImageRequest OpenAI 图像生成请求体
- Model: 模型名称，支持后缀指定尺寸，如 banana-16:9
- Prompt: 图像描述提示词
- N: 生成图片数量，默认为 1
- Size: 图像尺寸（可选，优先使用模型名后缀）
*/
type OpenAIImageRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	N      int    `json:"n"`
	Size   string `json:"size"`
	Image  string `json:"image,omitempty"`
}

/* OpenAIImageResponse OpenAI 图像生成响应体 */
type OpenAIImageResponse struct {
	Created int64             `json:"created"`
	Data    []OpenAIImageData `json:"data"`
}

/* OpenAIImageData OpenAI 图像数据项 */
type OpenAIImageData struct {
	URL string `json:"url"`
}

/* OpenAIErrorResponse OpenAI 错误响应体 */
type OpenAIErrorResponse struct {
	Error OpenAIError `json:"error"`
}

/* OpenAIError OpenAI 错误详情 */
type OpenAIError struct {
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Param   *string `json:"param"`
	Code    string  `json:"code"`
}

/* OpenAIModelList OpenAI 模型列表响应体 */
type OpenAIModelList struct {
	Object string        `json:"object"`
	Data   []OpenAIModel `json:"data"`
}

/* OpenAIModel OpenAI 模型信息 */
type OpenAIModel struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}
type CoffeeImageRequest struct {
	ImageMetadata CoffeeImageMetadata `json:"imageMetadata"`
	UserConfirmed bool                `json:"userConfirmed"`
}
type CoffeeImageMetadata struct {
	Prompt     string   `json:"prompt"`
	ModelType  string   `json:"modelType"`
	Width      int      `json:"width"`
	Height     int      `json:"height"`
	Base64Imgs []string `json:"base64_imgs,omitempty"`
}
type CoffeeGenerationResponse struct {
	IsUnlocked bool `json:"isUnlocked"`
	Metadata   struct {
		Prompt      string `json:"prompt"`
		Width       int    `json:"width"`
		Height      int    `json:"height"`
		ModelType   string `json:"modelType"`
		Status      string `json:"status"`
		QueryParams struct {
			PromptID  string `json:"prompt_id"`
			Signature string `json:"signature"`
		} `json:"queryParams"`
	} `json:"metadata"`
}

type CoffeeResultResponse struct {
	Image *string `json:"image"`
}

/* CoffeeUpscaleRequest 超分请求体 */
type CoffeeUpscaleRequest struct {
	Model     string `json:"model"`
	Base64Img string `json:"base64_img"`
}

/* CoffeeUpscaleResponse 超分响应体（prompt_id 在顶层） */
type CoffeeUpscaleResponse struct {
	PromptID  string `json:"prompt_id"`
	Signature string `json:"signature"`
}

/* CoffeeDescribeRequest 图片描述请求体 */
type CoffeeDescribeRequest struct {
	Lang        string `json:"lang"`
	Base64Image string `json:"base64Image"`
}

type CaptchaTaskResponse struct {
	TaskID string `json:"taskId"`
}
type CaptchaResultResponse struct {
	Solution struct {
		Token string `json:"token"`
	} `json:"solution"`
}

type ColorHandler struct {
	level slog.Level
}

func (h *ColorHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *ColorHandler) Handle(_ context.Context, r slog.Record) error {
	t := r.Time.Format("15:04:05")
	var icon, lc string
	switch {
	case r.Level >= slog.LevelError:
		icon, lc = "✗", cRed
	case r.Level >= slog.LevelWarn:
		icon, lc = "⚠", cYellow
	default:
		icon, lc = "✓", cGreen
	}

	var parts []string
	r.Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case "err":
			parts = append(parts, fmt.Sprintf("%s%v%s", cRed, a.Value, cReset))
		default:
			parts = append(parts, fmt.Sprintf("%s%s%s=%v", cGray, a.Key, cReset, a.Value))
		}
		return true
	})

	attrs := ""
	if len(parts) > 0 {
		attrs = " " + strings.Join(parts, " ")
	}

	fmt.Fprintf(os.Stderr, "%s%s%s %s%s%s %s%s\n",
		cGray, t, cReset,
		lc, icon, cReset,
		r.Message, attrs)
	return nil
}

func (h *ColorHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *ColorHandler) WithGroup(_ string) slog.Handler      { return h }

/* ==================== HTTP 客户端 ==================== */

var httpClient = &http.Client{
	Timeout: 120 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:        300,
		MaxIdleConnsPerHost: 50,
		MaxConnsPerHost:     0,
		IdleConnTimeout:     90 * time.Second,
		ForceAttemptHTTP2:   true,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
	},
}

func setCommonHeaders(req *http.Request) {
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8,en-GB;q=0.7,en-US;q=0.6")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("DNT", "1")
	req.Header.Set("Pragma", "no-cache")
	req.Header.Set("Sec-Ch-Ua", `"Not(A:Brand";v="8", "Chromium";v="144", "Microsoft Edge";v="144"`)
	req.Header.Set("Sec-Ch-Ua-Mobile", "?0")
	req.Header.Set("Sec-Ch-Ua-Platform", `"Windows"`)
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("User-Agent", userAgent)
}

func solveCaptcha(solverURL string) (string, error) {
	taskURL := fmt.Sprintf("%s/turnstile?url=%s&sitekey=%s",
		solverURL,
		url.QueryEscape(turnstileSiteURL),
		url.QueryEscape(turnstileSiteKey),
	)

	resp, err := httpClient.Get(taskURL)
	if err != nil {
		return "", fmt.Errorf("创建验证码任务失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("读取任务响应失败: %w", err)
	}

	var taskResp CaptchaTaskResponse
	if err := json.Unmarshal(body, &taskResp); err != nil {
		return "", fmt.Errorf("解析任务响应失败: %w, body: %s", err, string(body))
	}
	if taskResp.TaskID == "" {
		return "", fmt.Errorf("任务ID为空, 响应: %s", string(body))
	}

	/* 初始等待 2 秒后以 500ms 间隔轮询，更快获取结果 */
	time.Sleep(2 * time.Second)
	for i := 0; i < 120; i++ {
		resultURL := fmt.Sprintf("%s/result?id=%s", solverURL, taskResp.TaskID)
		resp, err := httpClient.Get(resultURL)
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}

		var result CaptchaResultResponse
		if err := json.Unmarshal(body, &result); err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		token := result.Solution.Token
		if token != "" && token != "CAPTCHA_FAIL" {
			return token, nil
		}
		if token == "CAPTCHA_FAIL" {
			return "", fmt.Errorf("验证码解决失败")
		}
		time.Sleep(500 * time.Millisecond)
	}
	return "", fmt.Errorf("验证码获取超时")
}

type CaptchaPool struct {
	tokens     chan timedToken
	solverURLs []string
	min        int
	max        int
	stopCh     chan struct{}
}

func newCaptchaPool(solverURLs []string, workers, poolMin, poolMax int) *CaptchaPool {
	if poolMin <= 0 {
		poolMin = 1
	}
	if poolMax <= poolMin {
		poolMax = poolMin * 3
	}
	p := &CaptchaPool{
		tokens:     make(chan timedToken, poolMax),
		solverURLs: solverURLs,
		min:        poolMin,
		max:        poolMax,
		stopCh:     make(chan struct{}),
	}
	for i := 0; i < workers; i++ {
		go p.worker(solverURLs[i%len(solverURLs)])
	}
	go p.maintain()
	slog.Info("令牌池已初始化", "min", poolMin, "max", poolMax, "workers", workers)
	return p
}

/*
maintain 定期维护令牌池：
- 清理过期令牌，腾出空间让 worker 补充新鲜令牌
- 30 秒报告一次池状态
*/
func (p *CaptchaPool) maintain() {
	statusTicker := time.NewTicker(30 * time.Second)
	cleanTicker := time.NewTicker(captchaTokenTTL / 3)
	defer statusTicker.Stop()
	defer cleanTicker.Stop()
	for {
		select {
		case <-cleanTicker.C:
			p.purgeExpired()
		case <-statusTicker.C:
			slog.Info("令牌池状态", "pool", len(p.tokens), "cap", cap(p.tokens))
		case <-p.stopCh:
			return
		}
	}
}

/* purgeExpired 非阻塞地从池中取出并丢弃所有过期令牌 */
func (p *CaptchaPool) purgeExpired() {
	purged := 0
	var kept []timedToken
	for {
		select {
		case tt := <-p.tokens:
			if time.Since(tt.createdAt) > captchaTokenTTL {
				purged++
			} else {
				kept = append(kept, tt)
			}
		default:
			/* 放回未过期的令牌 */
			for _, tt := range kept {
				select {
				case p.tokens <- tt:
				default:
				}
			}
			if purged > 0 {
				slog.Info("清理过期令牌", "purged", purged, "kept", len(kept), "pool", len(p.tokens))
			}
			return
		}
	}
}

func (p *CaptchaPool) worker(solverURL string) {
	for {
		select {
		case <-p.stopCh:
			return
		default:
		}
		/* 池已满时短暂等待，快速响应消费 */
		if len(p.tokens) >= p.max {
			time.Sleep(1 * time.Second)
			continue
		}
		token, err := solveCaptcha(solverURL)
		if err != nil {
			slog.Warn("验证码求解失败", "err", err)
			time.Sleep(1 * time.Second)
			continue
		}
		select {
		case p.tokens <- timedToken{token: token, createdAt: time.Now()}:
			slog.Info("令牌入池", "pool", len(p.tokens), "cap", cap(p.tokens))
		case <-p.stopCh:
			return
		}
	}
}

/*
get 从池中获取一个未过期的验证码令牌
过期令牌自动丢弃并重新获取，支持 context 超时控制
*/
func (p *CaptchaPool) get(ctx context.Context) (string, error) {
	for {
		select {
		case tt := <-p.tokens:
			if time.Since(tt.createdAt) > captchaTokenTTL {
				slog.Warn("丢弃过期令牌", "age", time.Since(tt.createdAt).Round(time.Second), "pool", len(p.tokens))
				continue
			}
			slog.Info("令牌出池", "pool", len(p.tokens), "cap", cap(p.tokens), "age", time.Since(tt.createdAt).Round(time.Second))
			return tt.token, nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

func (p *CaptchaPool) stop() {
	close(p.stopCh)
}

func findSolverDir(customDir string) string {
	if customDir != "" {
		if info, err := os.Stat(customDir); err == nil && info.IsDir() {
			return customDir
		}
		return ""
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Join(filepath.Dir(exe), "solver")
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
	}
	if info, err := os.Stat("solver"); err == nil && info.IsDir() {
		return "solver"
	}
	return ""
}

/* isSolverReachable 检测 Solver 服务是否可连接 */
func isSolverReachable(solverURL string) bool {
	u, err := url.Parse(solverURL)
	if err != nil {
		return false
	}
	checkURL := fmt.Sprintf("%s://%s/result?id=health", u.Scheme, u.Host)
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(checkURL)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}

/* runCmdQuiet 静默执行命令，失败时返回包含 stderr 信息的 error */
func runCmdQuiet(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = io.Discard
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, errBuf.String())
	}
	return nil
}

/* installSolverDeps 安装 Solver 的 Python 依赖和浏览器 */
func installSolverDeps(solverDir string) error {
	uvPath, uvErr := exec.LookPath("uv")
	pipPath, pipErr := exec.LookPath("pip")

	if uvErr != nil && pipErr != nil {
		return fmt.Errorf("未找到 uv 或 pip，请至少安装其中一个\n  uv: https://docs.astral.sh/uv/getting-started/installation/")
	}

	if uvErr == nil {
		if err := runCmdQuiet(uvPath, "sync", "--project", solverDir); err != nil {
			slog.Warn("uv sync 失败，尝试 pip", "err", err)
			if pipErr == nil {
				reqFile := filepath.Join(solverDir, "requirements.txt")
				if err := runCmdQuiet(pipPath, "install", "-q", "-r", reqFile); err != nil {
					return fmt.Errorf("pip install 也失败: %w", err)
				}
			} else {
				return fmt.Errorf("uv sync 失败且 pip 不可用: %w", err)
			}
		}
	} else {
		reqFile := filepath.Join(solverDir, "requirements.txt")
		if err := runCmdQuiet(pipPath, "install", "-q", "-r", reqFile); err != nil {
			return fmt.Errorf("pip install 失败: %w", err)
		}
	}

	/* 安装 patchright 浏览器 */
	if uvErr == nil {
		if err := runCmdQuiet(uvPath, "run", "--project", solverDir, "patchright", "install", "chromium"); err != nil {
			_ = runCmdQuiet("patchright", "install", "chromium")
		}
	} else {
		_ = runCmdQuiet("patchright", "install", "chromium")
	}

	/* 安装 camoufox 浏览器 */
	if uvErr == nil {
		if err := runCmdQuiet(uvPath, "run", "--project", solverDir, "python", "-m", "camoufox", "fetch"); err != nil {
			_ = runCmdQuiet("python", "-m", "camoufox", "fetch")
		}
	} else {
		_ = runCmdQuiet("python", "-m", "camoufox", "fetch")
	}

	return nil
}

/*
startSolver 启动 Turnstile Solver 子进程
自动安装依赖和浏览器，返回 cleanup 函数用于停止
*/
func startSolver(solverDir string, browsers int, port string) (cleanup func(), err error) {
	if err := installSolverDeps(solverDir); err != nil {
		return nil, err
	}

	var cmd *exec.Cmd
	uvPath, uvErr := exec.LookPath("uv")
	if uvErr == nil {
		args := []string{
			"run", "--project", solverDir,
			"python", "api_solver.py",
			"--thread", fmt.Sprintf("%d", browsers),
			"--host", "127.0.0.1",
			"--browser_type", "camoufox",
			"--port", port,
		}
		cmd = exec.Command(uvPath, args...)
	} else {
		pythonPath := "python"
		if p, err := exec.LookPath("python3"); err == nil {
			pythonPath = p
		}
		cmd = exec.Command(pythonPath,
			"api_solver.py",
			"--thread", fmt.Sprintf("%d", browsers),
			"--host", "127.0.0.1",
			"--browser_type", "camoufox",
			"--port", port,
		)
	}

	cmd.Dir = solverDir
	cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8")
	cmd.Stdout = io.Discard

	stderrPipe, pipeErr := cmd.StderrPipe()
	if pipeErr != nil {
		cmd.Stderr = io.Discard
	}

	setSysProcAttr(cmd)

	slog.Info("启动 Solver", "browsers", browsers, "port", port)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动 solver 失败: %w", err)
	}

	var stderrBuf bytes.Buffer
	if pipeErr == nil {
		go func() {
			buf := make([]byte, 4096)
			for {
				n, err := stderrPipe.Read(buf)
				if n > 0 {
					stderrBuf.Write(buf[:n])
					if stderrBuf.Len() > 2048 {
						b := stderrBuf.Bytes()
						stderrBuf.Reset()
						stderrBuf.Write(b[len(b)-2048:])
					}
				}
				if err != nil {
					return
				}
			}
		}()
	}

	exitCh := make(chan error, 1)
	go func() { exitCh <- cmd.Wait() }()

	var once sync.Once
	cleanup = func() {
		once.Do(func() {
			if cmd.Process == nil {
				return
			}
			slog.Info("正在停止 Solver 进程树...")
			killProcessTree(cmd)
			slog.Info("Solver 已停止")
		})
	}

	/* 等待 solver 就绪 */
	checkURL := fmt.Sprintf("http://127.0.0.1:%s/result?id=health", port)
	ready := false
	for i := 0; i < 120; i++ {
		select {
		case exitErr := <-exitCh:
			errMsg := stderrBuf.String()
			if errMsg == "" {
				errMsg = "无 stderr 输出"
			}
			return nil, fmt.Errorf("solver 进程已退出(%v):\n%s", exitErr, errMsg)
		case <-time.After(1 * time.Second):
		}
		resp, err := http.Get(checkURL)
		if err == nil {
			resp.Body.Close()
			ready = true
			break
		}
	}
	if !ready {
		cleanup()
		errMsg := stderrBuf.String()
		if errMsg != "" {
			return nil, fmt.Errorf("solver 启动超时（120s）:\n%s", errMsg)
		}
		return nil, fmt.Errorf("solver 启动超时（120s），请检查 Python 环境")
	}
	slog.Info("Solver 就绪")
	return cleanup, nil
}

func ensureSolver(solverURL string, browsers int, solverDirOverride string) (cleanup func(), actualBrowsers int, err error) {
	if isSolverReachable(solverURL) {
		slog.Info("Solver 已在运行", "url", solverURL)
		return nil, browsers, nil
	}

	slog.Warn("无法连接 Solver，尝试自动启动本地 Solver...", "url", solverURL)

	solverDir := findSolverDir(solverDirOverride)
	if solverDir == "" {
		return nil, 0, fmt.Errorf("无法连接 Solver(%s) 且未找到 solver/ 目录，无法自动启动", solverURL)
	}

	if browsers <= 0 {
		browsers = 2
	}

	port := "5072"
	if u, err := url.Parse(solverURL); err == nil && u.Port() != "" {
		port = u.Port()
	}

	cleanup, err = startSolver(solverDir, browsers, port)
	if err != nil {
		return nil, 0, err
	}
	return cleanup, browsers, nil
}

/* ==================== 模型名称解析 ==================== */

/*
parseModel 从模型名称中解析出模型类型和图像尺寸
支持格式：
  - "banana"       → modelType=banana, size=默认(3:2)
  - "banana-16:9"  → modelType=banana, size=16:9(1344x768)
  - "banana-1:1"   → modelType=banana, size=1:1(1024x1024)
*/
func parseModel(model string) (modelType string, size ImageSize) {
	modelType = "banana"
	size = defaultSize

	if model == "" {
		return
	}

	/* 检查模型名是否以已知的比例后缀结尾 */
	for ratio, s := range sizeMap {
		suffix := "-" + ratio
		if strings.HasSuffix(model, suffix) {
			modelType = strings.TrimSuffix(model, suffix)
			if modelType == "" {
				modelType = "banana"
			}
			size = s
			return
		}
	}

	/* 无已知后缀，整个字符串作为模型类型 */
	modelType = model
	return
}

/* ==================== CoffeeCat API 客户端 ==================== */

/* CoffeePromptRequest CoffeeCat 提示词优化请求体 */
type CoffeePromptRequest struct {
	UserInput string `json:"userInput"`
	Sfw       bool   `json:"sfw"`
	ModelType string `json:"modelType"`
}

/*
optimizePrompt 调用 CoffeeCat 提示词优化接口
将用户输入的简短描述优化为详细的生图提示词
*/
func optimizePrompt(sessionToken, prompt, modelType string) (string, error) {
	/* CoffeeCat 要求最少 10 个字符，不足时用句号补齐（空格会被 API 拒绝） */
	paddedPrompt := prompt
	if len([]rune(paddedPrompt)) < 10 {
		paddedPrompt = paddedPrompt + strings.Repeat(".", 10-len([]rune(paddedPrompt)))
	}

	reqBody := CoffeePromptRequest{
		UserInput: paddedPrompt,
		Sfw:       true,
		ModelType: modelType,
	}

	bodyData, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("序列化请求失败: %w", err)
	}

	req, err := http.NewRequest("POST", coffeePromptURL, bytes.NewReader(bodyData))
	if err != nil {
		return "", fmt.Errorf("创建请求失败: %w", err)
	}

	setCommonHeaders(req)
	req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	req.Header.Set("Origin", coffeeBaseURL)
	req.Header.Set("Referer", coffeeBaseURL+"/ai-image")
	req.Header.Set("Cookie", "__Secure-next-auth.session-token="+sessionToken)

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("读取响应失败: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("优化失败, 状态码: %d, 响应: %s", resp.StatusCode, string(body))
	}

	optimized := strings.TrimSpace(string(body))
	if optimized == "" {
		return prompt, nil
	}

	/* 尝试从 JSON 中提取 positive 字段（接口可能返回 {"positive":"..."}） */
	var parsed struct {
		Positive string `json:"positive"`
	}
	if err := json.Unmarshal([]byte(optimized), &parsed); err == nil && parsed.Positive != "" {
		return parsed.Positive, nil
	}

	return optimized, nil
}

/*
generateImage 调用 CoffeeCat 生图接口
返回包含 prompt_id 和 signature 的响应，用于后续轮询
*/
func generateImage(sessionToken, captchaToken, prompt, modelType string, width, height int, base64Imgs []string) (*CoffeeGenerationResponse, error) {
	reqBody := CoffeeImageRequest{
		ImageMetadata: CoffeeImageMetadata{
			Prompt:     prompt,
			ModelType:  modelType,
			Width:      width,
			Height:     height,
			Base64Imgs: base64Imgs,
		},
		UserConfirmed: false,
	}

	bodyData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("序列化请求失败: %w", err)
	}

	genURL := fmt.Sprintf(coffeeGenURLFmt, modelType)
	req, err := http.NewRequest("POST", genURL, bytes.NewReader(bodyData))
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}

	setCommonHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", coffeeBaseURL)
	req.Header.Set("Referer", coffeeBaseURL+"/ai-image")
	req.Header.Set("Cf-Turnstile-Token", captchaToken)
	req.Header.Set("Cookie", "__Secure-next-auth.session-token="+sessionToken)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("生成失败, 状态码: %d, 响应: %s", resp.StatusCode, string(body))
	}

	var genResp CoffeeGenerationResponse
	if err := json.Unmarshal(body, &genResp); err != nil {
		return nil, fmt.Errorf("解析响应失败: %w, body: %s", err, string(body))
	}

	return &genResp, nil
}

/*
pollImageResult 轮询等待图像生成完成
最长等待约 5 分钟（150 次轮询，每次间隔 2 秒）
*/
func pollImageResult(sessionToken, promptID, signature string) (string, error) {
	pollURL := fmt.Sprintf("%s?prompt_id=%s", coffeePollURL, url.QueryEscape(promptID))

	for i := 0; i < 150; i++ {
		req, err := http.NewRequest("GET", pollURL, nil)
		if err != nil {
			return "", fmt.Errorf("创建轮询请求失败: %w", err)
		}

		setCommonHeaders(req)
		req.Header.Set("Referer", coffeeBaseURL+"/ai-image")
		req.Header.Set("X-Signature", signature)
		req.Header.Set("Cookie", "__Secure-next-auth.session-token="+sessionToken)

		resp, err := httpClient.Do(req)
		if err != nil {
			time.Sleep(2 * time.Second)
			continue
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			time.Sleep(2 * time.Second)
			continue
		}

		/* 如果返回 null 或空内容，表示任务尚未完成 */
		trimmed := strings.TrimSpace(string(body))
		if trimmed == "null" || trimmed == "" {
			time.Sleep(2 * time.Second)
			continue
		}

		var result CoffeeResultResponse
		if err := json.Unmarshal(body, &result); err != nil {
			time.Sleep(2 * time.Second)
			continue
		}

		if result.Image != nil && *result.Image != "" {
			return *result.Image, nil
		}

		time.Sleep(2 * time.Second)
	}

	return "", fmt.Errorf("轮询超时（5分钟），图像未生成完成")
}

/*
downloadImageToBase64 从 URL 下载图片并转换为 base64 data URL
支持 http/https 图片链接，返回格式为 data:image/xxx;base64,...
*/
func downloadImageToBase64(imageURL string) (string, error) {
	req, err := http.NewRequest("GET", imageURL, nil)
	if err != nil {
		return "", fmt.Errorf("创建下载请求失败: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("下载图片失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载图片失败, 状态码: %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("读取图片数据失败: %w", err)
	}

	/* 根据 Content-Type 确定 MIME 类型，兜底为 image/jpeg */
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" || !strings.HasPrefix(contentType, "image/") {
		contentType = "image/jpeg"
	}

	encoded := base64.StdEncoding.EncodeToString(data)
	return fmt.Sprintf("data:%s;base64,%s", contentType, encoded), nil
}

/*
resolveImageToBase64 将图片引用统一转为 base64 data URL
- 已经是 data:... 格式的直接返回
- http/https URL 自动下载转换
*/
func resolveImageToBase64(ref string) (string, error) {
	if strings.HasPrefix(ref, "data:") {
		return ref, nil
	}
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		slog.Info("下载远程图片转 base64", "url", ref[:min(len(ref), 80)])
		return downloadImageToBase64(ref)
	}
	return ref, nil
}

/*
resolveAllImages 批量将图片引用转为 base64
*/
func resolveAllImages(refs []string) ([]string, error) {
	result := make([]string, 0, len(refs))
	for _, ref := range refs {
		resolved, err := resolveImageToBase64(ref)
		if err != nil {
			return nil, err
		}
		result = append(result, resolved)
	}
	return result, nil
}

/*
upscaleImage 调用 CoffeeCat 超分接口
返回 prompt_id 和 signature 用于轮询
*/
func upscaleImage(sessionToken, captchaToken, base64Img, model string) (*CoffeeUpscaleResponse, error) {
	if model == "" {
		model = defaultUpscaleModel
	}

	/* base64Img 需要去掉 data:xxx;base64, 前缀 */
	rawBase64 := base64Img
	if idx := strings.Index(base64Img, ","); idx >= 0 {
		rawBase64 = base64Img[idx+1:]
	}

	reqBody := CoffeeUpscaleRequest{
		Model:     model,
		Base64Img: rawBase64,
	}

	bodyData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("序列化请求失败: %w", err)
	}

	req, err := http.NewRequest("POST", coffeeUpscaleURL, bytes.NewReader(bodyData))
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}

	setCommonHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", coffeeBaseURL)
	req.Header.Set("Referer", coffeeBaseURL+"/ai-image/upscale")
	req.Header.Set("X-Turnstile-Token", captchaToken)
	req.Header.Set("Cookie", "__Secure-next-auth.session-token="+sessionToken)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("超分失败, 状态码: %d, 响应: %s", resp.StatusCode, string(body))
	}

	var upResp CoffeeUpscaleResponse
	if err := json.Unmarshal(body, &upResp); err != nil {
		return nil, fmt.Errorf("解析响应失败: %w, body: %s", err, string(body))
	}

	return &upResp, nil
}

/*
describeImage 调用 CoffeeCat 图片描述接口
输入图片 base64，返回英文描述/提示词
*/
func describeImage(sessionToken, captchaToken, base64Img string) (string, error) {
	reqBody := CoffeeDescribeRequest{
		Lang:        "en",
		Base64Image: base64Img,
	}

	bodyData, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("序列化请求失败: %w", err)
	}

	req, err := http.NewRequest("POST", coffeeDescribeURL, bytes.NewReader(bodyData))
	if err != nil {
		return "", fmt.Errorf("创建请求失败: %w", err)
	}

	setCommonHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", coffeeBaseURL)
	req.Header.Set("Referer", coffeeBaseURL+"/ai-image/reverse-prompt")
	req.Header.Set("Cf-Turnstile-Token", captchaToken)
	req.Header.Set("Cookie", "__Secure-next-auth.session-token="+sessionToken)

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("读取响应失败: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("描述失败, 状态码: %d, 响应: %s", resp.StatusCode, string(body))
	}

	return strings.TrimSpace(string(body)), nil
}

type Config struct {
	Port      int      `json:"port"`
	APIKeys   []string `json:"api_keys"`
	SolverURL string   `json:"solver_url"`
	SolverDir string   `json:"solver_dir"`
	Workers   int      `json:"workers"`
	Browsers  int      `json:"browsers"`
	PoolMin   int      `json:"pool_min"`
	PoolMax   int      `json:"pool_max"`
}

/* defaultConfig 返回默认配置 */
func defaultConfig() Config {
	return Config{
		Port:      8080,
		SolverURL: "http://127.0.0.1:5072",
		Workers:   2,
		Browsers:  2,
		PoolMin:   1,
		PoolMax:   6,
	}
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			cfg := defaultConfig()
			generated, _ := json.MarshalIndent(cfg, "", "  ")
			_ = os.WriteFile(path, generated, 0644)
			return nil, fmt.Errorf("配置文件不存在，已生成默认配置: %s\n请根据需要修改后重新启动", path)
		}
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}

	cfg := defaultConfig()
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}

	if cfg.Port <= 0 {
		cfg.Port = 8080
	}

	return &cfg, nil
}

type Server struct {
	pool      *CaptchaPool
	config    *Config
	apiKeySet map[string]struct{}
}

func (s *Server) sendError(w http.ResponseWriter, status int, message, errType, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(OpenAIErrorResponse{
		Error: OpenAIError{
			Message: message,
			Type:    errType,
			Param:   nil,
			Code:    code,
		},
	})
}

func (s *Server) authenticate(r *http.Request) bool {
	/* 未配置 api_keys 时免鉴权 */
	if len(s.apiKeySet) == 0 {
		return true
	}
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return false
	}
	key := strings.TrimPrefix(auth, "Bearer ")
	if key == "" {
		return false
	}

	_, ok := s.apiKeySet[key]
	return ok
}

func (s *Server) getSessionToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	return ""
}
func (s *Server) handleImageGeneration(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.sendError(w, http.StatusMethodNotAllowed, "仅支持 POST 方法", "invalid_request_error", "method_not_allowed")
		return
	}

	if !s.authenticate(r) {
		s.sendError(w, http.StatusUnauthorized, "无效的 API Key", "authentication_error", "invalid_api_key")
		return
	}

	sessionToken := s.getSessionToken(r)
	if sessionToken == "" {
		s.sendError(w, http.StatusUnauthorized, "缺少 session token，请在配置文件或 Bearer token 中提供", "authentication_error", "missing_session")
		return
	}

	var req OpenAIImageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.sendError(w, http.StatusBadRequest, "无效的请求体: "+err.Error(), "invalid_request_error", "invalid_body")
		return
	}

	if req.Prompt == "" {
		s.sendError(w, http.StatusBadRequest, "prompt 不能为空", "invalid_request_error", "missing_prompt")
		return
	}

	if req.N <= 0 {
		req.N = 1
	}

	modelType, size := parseModel(req.Model)

	isImg2Img := req.Image != ""
	slog.Info("收到图像生成请求",
		"model", req.Model,
		"modelType", modelType,
		"size", fmt.Sprintf("%dx%d", size.Width, size.Height),
		"n", req.N,
		"img2img", isImg2Img,
	)

	var images []OpenAIImageData
	headersFlushed := false

	for i := 0; i < req.N; i++ {
		/* 获取验证码令牌 */
		ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
		captchaToken, err := s.pool.get(ctx)
		cancel()
		if err != nil {
			slog.Error("获取验证码超时", "err", err)
			s.sendError(w, http.StatusInternalServerError, "获取验证码超时: "+err.Error(), "server_error", "captcha_timeout")
			return
		}

		slog.Info("验证码已获取，正在提交生图任务", "index", i+1)

		/* 调用 CoffeeCat 生图接口（image 字段非空时为图生图） */
		var base64Imgs []string
		if req.Image != "" {
			resolved, err := resolveImageToBase64(req.Image)
			if err != nil {
				slog.Error("图片下载失败", "err", err)
				s.sendError(w, http.StatusBadRequest, "图片下载失败: "+err.Error(), "invalid_request_error", "image_download_failed")
				return
			}
			base64Imgs = []string{resolved}
		}
		genResp, err := generateImage(sessionToken, captchaToken, req.Prompt, modelType, size.Width, size.Height, base64Imgs)
		if err != nil {
			slog.Error("图像生成请求失败", "err", err)
			s.sendError(w, http.StatusInternalServerError, "图像生成失败: "+err.Error(), "server_error", "generation_failed")
			return
		}

		promptID := genResp.Metadata.QueryParams.PromptID
		signature := genResp.Metadata.QueryParams.Signature

		if promptID == "" {
			s.sendError(w, http.StatusInternalServerError, "未获取到任务ID", "server_error", "missing_prompt_id")
			return
		}

		slog.Info("生图任务已提交，开始轮询结果", "promptID", promptID)
		if !headersFlushed {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			headersFlushed = true
		}

		/* 轮询获取结果 */
		imageURL, err := pollImageResult(sessionToken, promptID, signature)
		if err != nil {
			slog.Error("轮询结果失败", "err", err)
			continue
		}

		images = append(images, OpenAIImageData{URL: imageURL})
		slog.Info("图像生成完成", "index", i+1, "url", imageURL)
	}

	resp := OpenAIImageResponse{
		Created: time.Now().Unix(),
		Data:    images,
	}

	if !headersFlushed {
		w.Header().Set("Content-Type", "application/json")
	}
	json.NewEncoder(w).Encode(resp)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

type ChatCompletionRequest struct {
	Model    string        `json:"model"`
	Messages []ChatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

/*
ChatMessage 聊天消息，Content 支持两种格式：
- 纯文本字符串: "hello"
- 多模态数组: [{"type":"text","text":"hello"},{"type":"image_url","image_url":{"url":"data:image/..."}}]
*/
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

/* ContentPart 多模态内容部分 */
type ContentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

/* ImageURL 图片 URL 结构 */
type ImageURL struct {
	URL string `json:"url"`
}

/* UnmarshalJSON 自定义反序列化，兼容 string 和 []ContentPart 两种 content 格式 */
func (m *ChatMessage) UnmarshalJSON(data []byte) error {
	type Alias struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	var a Alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	m.Role = a.Role

	/* 尝试解析为字符串 */
	var s string
	if err := json.Unmarshal(a.Content, &s); err == nil {
		m.Content = s
		return nil
	}

	/* 尝试解析为多模态数组，提取文本部分拼接 */
	var parts []ContentPart
	if err := json.Unmarshal(a.Content, &parts); err == nil {
		var texts []string
		for _, p := range parts {
			if p.Type == "text" && p.Text != "" {
				texts = append(texts, p.Text)
			}
		}
		m.Content = strings.Join(texts, "\n")
		return nil
	}

	/* 兜底：当作字符串处理 */
	m.Content = string(a.Content)
	return nil
}

/*
extractImages 从聊天消息中提取 base64 图片
返回所有 image_url 类型内容中的 data URL
*/
func extractImages(messages []ChatMessage, rawBody []byte) []string {
	type rawMsg struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	type rawReq struct {
		Messages []rawMsg `json:"messages"`
	}
	var raw rawReq
	if err := json.Unmarshal(rawBody, &raw); err != nil {
		return nil
	}

	var imgs []string
	for _, msg := range raw.Messages {
		var parts []ContentPart
		if err := json.Unmarshal(msg.Content, &parts); err != nil {
			continue
		}
		for _, p := range parts {
			if p.Type == "image_url" && p.ImageURL != nil && p.ImageURL.URL != "" {
				imgs = append(imgs, p.ImageURL.URL)
			}
		}
	}
	return imgs
}

/* ChatCompletionResponse OpenAI Chat Completions 响应体 */
type ChatCompletionResponse struct {
	ID      string              `json:"id"`
	Object  string              `json:"object"`
	Created int64               `json:"created"`
	Model   string              `json:"model"`
	Choices []ChatChoice        `json:"choices"`
	Usage   ChatCompletionUsage `json:"usage"`
}

/* ChatChoice 聊天选项 */
type ChatChoice struct {
	Index        int         `json:"index"`
	Message      ChatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

type ChatCompletionUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.sendError(w, http.StatusMethodNotAllowed, "仅支持 POST 方法", "invalid_request_error", "method_not_allowed")
		return
	}

	if !s.authenticate(r) {
		s.sendError(w, http.StatusUnauthorized, "无效的 API Key", "authentication_error", "invalid_api_key")
		return
	}

	sessionToken := s.getSessionToken(r)
	if sessionToken == "" {
		s.sendError(w, http.StatusUnauthorized, "缺少 session token，请在配置文件或 Bearer token 中提供", "authentication_error", "missing_session")
		return
	}

	/* 先读取原始 body，用于后续提取多模态图片 */
	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		s.sendError(w, http.StatusBadRequest, "读取请求体失败", "invalid_request_error", "read_body_failed")
		return
	}

	var req ChatCompletionRequest
	if err := json.Unmarshal(rawBody, &req); err != nil {
		s.sendError(w, http.StatusBadRequest, "无效的请求体: "+err.Error(), "invalid_request_error", "invalid_body")
		return
	}

	if len(req.Messages) == 0 {
		s.sendError(w, http.StatusBadRequest, "messages 不能为空", "invalid_request_error", "missing_messages")
		return
	}

	/* 提取最后一条用户消息 */
	var userPrompt string
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			userPrompt = req.Messages[i].Content
			break
		}
	}
	if userPrompt == "" {
		s.sendError(w, http.StatusBadRequest, "未找到用户消息", "invalid_request_error", "missing_user_message")
		return
	}

	/* 从多模态消息中提取图片（可能是 URL 或 base64） */
	rawImgs := extractImages(req.Messages, rawBody)
	base64Imgs, err := resolveAllImages(rawImgs)
	if err != nil {
		slog.Error("图片下载失败", "err", err)
		s.sendError(w, http.StatusBadRequest, "图片下载失败: "+err.Error(), "invalid_request_error", "image_download_failed")
		return
	}

	/* 根据模型名分发到不同处理逻辑 */
	isOptimize := strings.Contains(req.Model, "optimize")
	isDescribe := strings.Contains(req.Model, "describe")
	isUpscale := strings.Contains(req.Model, "upscale")

	if isDescribe {
		/* ====== 图片描述模式（输入图片，输出提示词） ====== */
		slog.Info("收到图片描述请求", "model", req.Model, "images", len(base64Imgs))

		if len(base64Imgs) == 0 {
			s.sendError(w, http.StatusBadRequest, "banana-describe 需要提供图片", "invalid_request_error", "missing_image")
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
		captchaToken, err := s.pool.get(ctx)
		cancel()
		if err != nil {
			s.sendError(w, http.StatusInternalServerError, "获取验证码超时: "+err.Error(), "server_error", "captcha_timeout")
			return
		}

		description, err := describeImage(sessionToken, captchaToken, base64Imgs[0])
		if err != nil {
			slog.Error("图片描述失败", "err", err)
			s.sendError(w, http.StatusInternalServerError, "图片描述失败: "+err.Error(), "server_error", "describe_failed")
			return
		}

		slog.Info("图片描述完成", "description", description[:min(len(description), 100)])
		s.sendChatResponse(w, req.Model, description)

	} else if isUpscale {
		/* ====== 超分模式（输入图片，输出超分后的图片） ====== */
		slog.Info("收到超分请求", "model", req.Model, "images", len(base64Imgs))

		if len(base64Imgs) == 0 {
			s.sendError(w, http.StatusBadRequest, "banana-upscale 需要提供图片", "invalid_request_error", "missing_image")
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
		captchaToken, err := s.pool.get(ctx)
		cancel()
		if err != nil {
			s.sendError(w, http.StatusInternalServerError, "获取验证码超时: "+err.Error(), "server_error", "captcha_timeout")
			return
		}

		upscaleCkpt := parseUpscaleModel(req.Model)
		slog.Info("超分模型", "ckpt", upscaleCkpt)
		upResp, err := upscaleImage(sessionToken, captchaToken, base64Imgs[0], upscaleCkpt)
		if err != nil {
			slog.Error("超分请求失败", "err", err)
			s.sendError(w, http.StatusInternalServerError, "超分失败: "+err.Error(), "server_error", "upscale_failed")
			return
		}

		if upResp.PromptID == "" {
			s.sendError(w, http.StatusInternalServerError, "未获取到超分任务ID", "server_error", "missing_prompt_id")
			return
		}

		slog.Info("超分任务已提交，开始轮询", "promptID", upResp.PromptID)

		/* 先 flush 响应头 */
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}

		imageResult, err := pollImageResult(sessionToken, upResp.PromptID, upResp.Signature)
		if err != nil {
			slog.Error("超分轮询失败", "err", err)
			s.sendChatResponseBody(w, req.Model, "超分失败: "+err.Error())
			return
		}

		slog.Info("超分完成")
		content := fmt.Sprintf("![upscaled](%s)", imageResult)
		s.sendChatResponseBody(w, req.Model, content)

	} else if isOptimize {
		/* ====== 提示词优化模式 ====== */
		slog.Info("收到提示词优化请求", "model", req.Model, "prompt", userPrompt)

		optimized, err := optimizePrompt(sessionToken, userPrompt, "banana")
		if err != nil {
			slog.Error("提示词优化失败", "err", err)
			s.sendError(w, http.StatusInternalServerError, "提示词优化失败: "+err.Error(), "server_error", "optimize_failed")
			return
		}

		slog.Info("提示词优化完成", "optimized", optimized)
		s.sendChatResponse(w, req.Model, optimized)
	} else {
		/* ====== 生图模式（通过 chat 接口） ====== */
		modelType, size := parseModel(req.Model)

		slog.Info("收到 chat 生图请求",
			"model", req.Model,
			"modelType", modelType,
			"size", fmt.Sprintf("%dx%d", size.Width, size.Height),
			"img2img", len(base64Imgs) > 0,
		)

		/* 获取验证码令牌 */
		ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
		captchaToken, err := s.pool.get(ctx)
		cancel()
		if err != nil {
			slog.Error("获取验证码超时", "err", err)
			s.sendError(w, http.StatusInternalServerError, "获取验证码超时: "+err.Error(), "server_error", "captcha_timeout")
			return
		}

		slog.Info("验证码已获取，正在提交生图任务")

		genResp, err := generateImage(sessionToken, captchaToken, userPrompt, modelType, size.Width, size.Height, base64Imgs)
		if err != nil {
			slog.Error("图像生成请求失败", "err", err)
			s.sendError(w, http.StatusInternalServerError, "图像生成失败: "+err.Error(), "server_error", "generation_failed")
			return
		}

		promptID := genResp.Metadata.QueryParams.PromptID
		signature := genResp.Metadata.QueryParams.Signature

		if promptID == "" {
			s.sendError(w, http.StatusInternalServerError, "未获取到任务ID", "server_error", "missing_prompt_id")
			return
		}

		slog.Info("生图任务已提交，开始轮询结果", "promptID", promptID)

		/* 任务提交成功，先 flush 响应头 */
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}

		imageURL, err := pollImageResult(sessionToken, promptID, signature)
		if err != nil {
			slog.Error("轮询结果失败", "err", err)
			/* 响应头已发送，写入错误信息到 body */
			s.sendChatResponseBody(w, req.Model, "图像生成失败: "+err.Error())
			return
		}

		slog.Info("图像生成完成", "url", imageURL)

		/* 以 Markdown 图片格式返回 */
		content := fmt.Sprintf("![image](%s)", imageURL)
		s.sendChatResponseBody(w, req.Model, content)
	}
}

/* sendChatResponse 发送完整 Chat Completions 响应（含 header） */
func (s *Server) sendChatResponse(w http.ResponseWriter, model, content string) {
	w.Header().Set("Content-Type", "application/json")
	s.sendChatResponseBody(w, model, content)
}

/* sendChatResponseBody 只写 Chat Completions 的 JSON body（header 已发送时使用） */
func (s *Server) sendChatResponseBody(w http.ResponseWriter, model, content string) {
	resp := ChatCompletionResponse{
		ID:      "chatcmpl-" + fmt.Sprintf("%d", time.Now().UnixNano()),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []ChatChoice{
			{
				Index: 0,
				Message: ChatMessage{
					Role:    "assistant",
					Content: content,
				},
				FinishReason: "stop",
			},
		},
		Usage: ChatCompletionUsage{
			PromptTokens:     0,
			CompletionTokens: 0,
			TotalTokens:      0,
		},
	}

	json.NewEncoder(w).Encode(resp)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

/*
handleModels 处理 GET /v1/models
返回所有支持的模型列表
*/
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.sendError(w, http.StatusMethodNotAllowed, "仅支持 GET 方法", "invalid_request_error", "method_not_allowed")
		return
	}

	now := time.Now().Unix()
	var models []OpenAIModel

	/* 文生图模型：banana + 尺寸后缀 */
	models = append(models, OpenAIModel{
		ID: "banana", Object: "model", Created: now, OwnedBy: "coffeecatai",
	})
	for ratio := range sizeMap {
		models = append(models, OpenAIModel{
			ID: "banana-" + ratio, Object: "model", Created: now, OwnedBy: "coffeecatai",
		})
	}

	/* 提示词优化模型（走 chat completions 接口） */
	models = append(models, OpenAIModel{
		ID: "banana-optimize", Object: "model", Created: now, OwnedBy: "coffeecatai",
	})

	/* 图片描述模型（输入图片，输出提示词） */
	models = append(models, OpenAIModel{
		ID: "banana-describe", Object: "model", Created: now, OwnedBy: "coffeecatai",
	})

	/* 超分模型（默认 + 各子模型） */
	models = append(models, OpenAIModel{
		ID: "banana-upscale", Object: "model", Created: now, OwnedBy: "coffeecatai",
	})
	for alias := range upscaleModelMap {
		models = append(models, OpenAIModel{
			ID: "banana-upscale-" + alias, Object: "model", Created: now, OwnedBy: "coffeecatai",
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(OpenAIModelList{
		Object: "list",
		Data:   models,
	})
}

/* corsMiddleware 为所有响应添加 CORS 头，支持跨域调用 */
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

/* ==================== 主入口 ==================== */

func main() {
	slog.SetDefault(slog.New(&ColorHandler{level: slog.LevelDebug}))

	/* 确定配置文件路径：优先使用命令行参数，否则使用可执行文件同目录下的 config.json */
	configPath := "config.json"
	if len(os.Args) > 1 {
		configPath = os.Args[1]
	} else if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "config.json")
		if _, err := os.Stat(candidate); err == nil {
			configPath = candidate
		}
	}

	/* 加载配置文件 */
	cfg, err := loadConfig(configPath)
	if err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}

	/* 构建 API Key 快速查找集合 */
	apiKeySet := make(map[string]struct{})
	for _, k := range cfg.APIKeys {
		k = strings.TrimSpace(k)
		if k != "" {
			apiKeySet[k] = struct{}{}
		}
	}

	/* 注册信号处理 */
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)

	/* 确保 Solver 可用 */
	firstURL := strings.Split(cfg.SolverURL, ",")[0]
	solverCleanup, actualBrowsers, err := ensureSolver(strings.TrimSpace(firstURL), cfg.Browsers, cfg.SolverDir)
	if err != nil {
		slog.Error("Solver 不可用", "err", err)
		os.Exit(1)
	}
	if solverCleanup != nil {
		defer solverCleanup()
	}

	workers := cfg.Workers
	if workers <= 0 {
		if actualBrowsers > 0 {
			workers = actualBrowsers
		} else {
			workers = 2
		}
	}

	/* 初始化验证码池 */
	solverURLs := strings.Split(cfg.SolverURL, ",")
	for i := range solverURLs {
		solverURLs[i] = strings.TrimSpace(solverURLs[i])
	}
	pool := newCaptchaPool(solverURLs, workers, cfg.PoolMin, cfg.PoolMax)
	defer pool.stop()

	server := &Server{
		pool:      pool,
		config:    cfg,
		apiKeySet: apiKeySet,
	}

	/* 路由注册 */
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/images/generations", server.handleImageGeneration)
	mux.HandleFunc("/v1/chat/completions", server.handleChatCompletions)
	mux.HandleFunc("/v1/models", server.handleModels)

	/* 后台信号监听 */
	go func() {
		<-sigCh
		slog.Info("收到中断信号，正在退出...")
		pool.stop()
		if solverCleanup != nil {
			solverCleanup()
		}
		os.Exit(0)
	}()

	portStr := strconv.Itoa(cfg.Port)
	slog.Info("监听地址: 0.0.0.0:" + portStr)
	slog.Info("Solver 地址: " + cfg.SolverURL)
	slog.Info("验证码并发数: " + fmt.Sprintf("%d", workers))
	if len(apiKeySet) > 0 {
		slog.Info(fmt.Sprintf("鉴权: 已启用 (%d 个 API Key)", len(apiKeySet)))
	} else {
		slog.Info("鉴权: 未启用（任何人可访问）")
	}
	slog.Info("  [图像生成] POST /v1/images/generations & /v1/chat/completions")
	slog.Info("  [提示词优化] POST /v1/chat/completions")
	slog.Info("  banana-optimize → 输入提示词, 输出优化后的提示词")

	if err := http.ListenAndServe(":"+portStr, corsMiddleware(mux)); err != nil {
		slog.Error("服务启动失败", "err", err)
		os.Exit(1)
	}
}
