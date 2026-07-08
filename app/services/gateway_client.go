// gateway_client.go — dev_pub_0.9：client 全部後端流量的單一 gRPC 連線
// （agent_gateway 的 ClientGateway service）。
//
// 取代了三個 REST client（agents/agent_graph/agent_embedding 直連）。每次呼叫
// 在 gRPC metadata 帶 x-api-key + x-device-id（gateway 端做「一 Key 一裝置」綁定）。
package services

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"pub_client/app/models"
	"pub_client/pb/gatewaypb"
)

// ErrHostDown gateway 連不上（主機關機 / 網路不通）。外網 client 啟動時的健康檢查用。
var ErrHostDown = errors.New("RAG主機已關機")

const uploadChunkBytes = 64 << 10 // 64KB/則，對齊 gateway 設計

// GatewayClient 對 agent_gateway 的 gRPC client。
type GatewayClient struct {
	conn     *grpc.ClientConn
	client   gatewaypb.ClientGatewayClient
	agentID  string
	apiKey   string
	deviceID string
}

// normalizeGatewayAddr 把使用者可能貼上的 scheme/尾斜線去掉——gRPC target 需要純
// host:port，帶 "https://" 會被當成主機名再補上預設埠 :443 而報 "too many colons"。
func normalizeGatewayAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	addr = strings.TrimPrefix(addr, "https://")
	addr = strings.TrimPrefix(addr, "http://")
	return strings.TrimRight(addr, "/")
}

// NewGatewayClient 建立連線（非阻塞 dial；gateway 沒起來時呼叫自然回錯）。
// addr 為 host:port（帶不帶 scheme 都可，會自動正規化）。空 addr 回 nil。
func NewGatewayClient(addr, agentID, apiKey, deviceID string, insecureTLS bool) *GatewayClient {
	addr = normalizeGatewayAddr(addr)
	if addr == "" {
		return nil
	}
	creds := credentials.NewTLS(&tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: insecureTLS, // #nosec G402 — 測試機自簽憑證；正式上線改 CA pinning
	})
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil // NewClient 只會因參數錯誤失敗；視同未設定
	}
	return &GatewayClient{
		conn:     conn,
		client:   gatewaypb.NewClientGatewayClient(conn),
		agentID:  agentID,
		apiKey:   apiKey,
		deviceID: deviceID,
	}
}

func (c *GatewayClient) Close() error { return c.conn.Close() }

func (c *GatewayClient) AgentID() string { return c.agentID }

// authCtx 附上認證 metadata。
func (c *GatewayClient) authCtx(ctx context.Context) context.Context {
	host, _ := os.Hostname()
	return metadata.AppendToOutgoingContext(ctx,
		"x-api-key", c.apiKey,
		"x-device-id", c.deviceID,
		"x-device-info", host,
	)
}

// ---- Ping（Test Connection，兼驗 API Key）----

// Ping 回傳（往返延遲, key 名稱, error）。
func (c *GatewayClient) Ping(ctx context.Context) (time.Duration, string, error) {
	start := time.Now()
	resp, err := c.client.Ping(c.authCtx(ctx), &gatewaypb.PingReq{})
	if err != nil {
		return 0, "", err
	}
	return time.Since(start), resp.GetKeyName(), nil
}

// HealthCheck 對 gateway 做一次 Ping 判斷主機是否在線。
//   - 連不上（Unavailable / DeadlineExceeded）→ 回 ErrHostDown（"RAG主機已關機"）
//   - 主機有回應（即使 API Key 無效 = Unauthenticated）→ 回 nil（主機沒關機）
//   - 其他錯誤 → 原樣回傳（呼叫端自行決定是否顯示）
func (c *GatewayClient) HealthCheck(ctx context.Context) error {
	_, _, err := c.Ping(ctx)
	if err == nil {
		return nil
	}
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded:
		return ErrHostDown
	case codes.Unauthenticated, codes.PermissionDenied:
		return nil // 主機活著，只是認證問題——不算關機
	default:
		return err
	}
}

// ---- Chat（介面對齊舊 APIClient.Chat，ChatEngine 不用改邏輯）----

func (c *GatewayClient) Chat(ctx context.Context, req models.ChatRequest) (*models.ChatResponse, error) {
	if req.AgentID == "" {
		req.AgentID = c.agentID
	}
	in := &gatewaypb.ChatReq{
		AgentId:        req.AgentID,
		SessionId:      req.SessionID,
		ContinuationId: req.ContinuationID,
		Temperature:    req.Temperature,
	}
	for _, m := range req.Messages {
		in.Messages = append(in.Messages, &gatewaypb.ChatMessage{Role: m.Role, Content: m.Content})
	}
	for _, t := range req.ClientTools {
		in.ClientTools = append(in.ClientTools, &gatewaypb.ChatToolSpec{
			Name: t.Name, Description: t.Description, SchemaJson: string(t.Schema),
		})
	}
	for _, r := range req.ToolResults {
		in.ToolResults = append(in.ToolResults, &gatewaypb.ChatToolResult{
			ToolCallId: r.ToolCallID, Name: r.Name, Content: r.Content, IsError: r.IsError,
		})
	}

	resp, err := c.client.Chat(c.authCtx(ctx), in)
	if err != nil {
		return nil, fmt.Errorf("chat: %w", err)
	}

	out := &models.ChatResponse{
		Status:         resp.GetStatus(),
		Content:        resp.GetContent(),
		SessionID:      resp.GetSessionId(),
		ContinuationID: resp.GetContinuationId(),
	}
	for _, tc := range resp.GetToolCalls() {
		call := models.ToolCall{ID: tc.GetId(), Name: tc.GetName()}
		if raw := tc.GetArgsJson(); raw != "" {
			if err := json.Unmarshal([]byte(raw), &call.Args); err != nil {
				return nil, fmt.Errorf("decode tool call args: %w", err)
			}
		}
		out.ToolCalls = append(out.ToolCalls, call)
	}
	if u := resp.GetUsage(); u != nil {
		out.Usage = &models.Usage{
			PromptTokens:     int(u.GetPromptTokens()),
			CompletionTokens: int(u.GetCompletionTokens()),
			TotalTokens:      int(u.GetTotalTokens()),
		}
	}
	return out, nil
}

// ChatStatus 輪詢某 session 目前處理到哪個 Agent 階段（供對話中狀態列顯示）。
// 回 active=false 表示 server 端當下沒在處理（例如等 client 工具），呼叫端顯示本地階段即可。
func (c *GatewayClient) ChatStatus(ctx context.Context, sessionID string) (stage string, active bool, err error) {
	resp, err := c.client.ChatStatus(c.authCtx(ctx), &gatewaypb.ChatStatusReq{SessionId: sessionID})
	if err != nil {
		return "", false, err
	}
	return resp.GetStage(), resp.GetActive(), nil
}

// ChatImage 送一張附加圖片到 server 做視覺辨識（抽文字 + 同時 ingest 進 RAG），回傳抽出的文字。
// client 把回傳文字併進當下這輪對話訊息，讓 agent「辨識」圖片內容。
func (c *GatewayClient) ChatImage(ctx context.Context, agentID, filename string, image []byte) (string, error) {
	resp, err := c.client.ChatImage(c.authCtx(ctx), &gatewaypb.ChatImageReq{
		AgentId: agentID, Filename: filename, Image: image,
	})
	if err != nil {
		return "", err
	}
	return resp.GetText(), nil
}

// ---- Upload / Job（client-streaming）----

func (c *GatewayClient) Upload(ctx context.Context, agentID string, filePaths, tags []string) (string, error) {
	if agentID == "" {
		agentID = c.agentID
	}
	for _, p := range filePaths {
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("stat %s: %w", p, err)
		}
	}

	stream, err := c.client.UploadIngest(c.authCtx(ctx))
	if err != nil {
		return "", fmt.Errorf("open upload stream: %w", err)
	}
	// 首則：meta
	if err := stream.Send(&gatewaypb.UploadChunk{
		Meta: &gatewaypb.UploadMeta{AgentId: agentID, Tags: tags},
	}); err != nil {
		return "", fmt.Errorf("send meta: %w", err)
	}
	// 檔案 chunk：邊讀邊送，記憶體佔用與檔案大小脫鉤
	buf := make([]byte, uploadChunkBytes)
	for _, p := range filePaths {
		f, ferr := os.Open(p)
		if ferr != nil {
			return "", fmt.Errorf("open %s: %w", p, ferr)
		}
		name := baseName(p)
		for {
			n, rerr := f.Read(buf)
			if n > 0 {
				if serr := stream.Send(&gatewaypb.UploadChunk{
					FileName: name,
					Data:     append([]byte(nil), buf[:n]...),
				}); serr != nil {
					f.Close()
					return "", fmt.Errorf("send chunk %s: %w", name, serr)
				}
			}
			if rerr == io.EOF {
				break
			}
			if rerr != nil {
				f.Close()
				return "", fmt.Errorf("read %s: %w", p, rerr)
			}
		}
		f.Close()
	}

	ack, err := stream.CloseAndRecv()
	if err != nil {
		return "", fmt.Errorf("upload: %w", err)
	}
	return ack.GetJobId(), nil
}

func baseName(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

func (c *GatewayClient) GetJob(ctx context.Context, id string) (*models.Job, error) {
	resp, err := c.client.GetIngestJob(c.authCtx(ctx), &gatewaypb.IngestJobReq{JobId: id})
	if err != nil {
		return nil, fmt.Errorf("get job: %w", err)
	}
	return &models.Job{
		ID:          resp.GetId(),
		Status:      resp.GetStatus(),
		TotalChunks: int(resp.GetTotalChunks()),
		DoneChunks:  int(resp.GetDoneChunks()),
		Error:       resp.GetError(),
	}, nil
}

// ---- Graph（介面對齊舊 GraphClient）----

func gwToEntity(e *gatewaypb.GraphEntity) models.GraphEntity {
	if e == nil {
		return models.GraphEntity{}
	}
	return models.GraphEntity{
		Name: e.GetName(), Category: e.GetCategory(), Description: e.GetDescription(),
		SourceFile: e.GetSourceFile(), Score: e.GetScore(),
	}
}

func gwToContext(c *gatewaypb.GraphContextResp) models.GraphContext {
	out := models.GraphContext{Center: gwToEntity(c.GetCenter())}
	for _, n := range c.GetNeighbors() {
		out.Neighbors = append(out.Neighbors, gwToEntity(n))
	}
	for _, r := range c.GetRelations() {
		out.Relations = append(out.Relations, models.GraphRelation{
			Source: r.GetSource(), Target: r.GetTarget(), Type: r.GetType(),
		})
	}
	return out
}

func gwToProposed(p *gatewaypb.GraphProposedRelation) models.GraphProposedRelation {
	return models.GraphProposedRelation{
		ID: p.GetId(), SourceEntity: p.GetSourceEntity(), TargetEntity: p.GetTargetEntity(),
		RelationType: p.GetRelationType(), Confidence: p.GetConfidence(),
		SourceFile: p.GetSourceFile(), ExtractedBy: p.GetExtractedBy(),
		Status: p.GetStatus(), CreatedAt: p.GetCreatedAt(),
	}
}

func (c *GatewayClient) SearchEntities(ctx context.Context, query string, limit int) ([]models.GraphEntity, error) {
	resp, err := c.client.GraphSearch(c.authCtx(ctx), &gatewaypb.GraphSearchReq{Query: query, Limit: int32(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]models.GraphEntity, 0, len(resp.GetEntities()))
	for _, e := range resp.GetEntities() {
		out = append(out, gwToEntity(e))
	}
	return out, nil
}

func (c *GatewayClient) GetLocalContext(ctx context.Context, entityName string, hops, limit int) (models.GraphContext, error) {
	resp, err := c.client.GraphContext(c.authCtx(ctx), &gatewaypb.GraphContextReq{
		EntityName: entityName, Hops: int32(hops), Limit: int32(limit),
	})
	if err != nil {
		return models.GraphContext{}, err
	}
	return gwToContext(resp), nil
}

func (c *GatewayClient) HybridSearch(ctx context.Context, query string, topK, hops int) (models.GraphHybridSearchResult, error) {
	resp, err := c.client.GraphHybridSearch(c.authCtx(ctx), &gatewaypb.GraphHybridSearchReq{
		Query: query, TopK: int32(topK), Hops: int32(hops),
	})
	if err != nil {
		return models.GraphHybridSearchResult{}, err
	}
	out := models.GraphHybridSearchResult{}
	for _, e := range resp.GetSeeds() {
		out.Seeds = append(out.Seeds, gwToEntity(e))
	}
	for _, cc := range resp.GetContext() {
		out.Context = append(out.Context, gwToContext(cc))
	}
	return out, nil
}

func (c *GatewayClient) ListProposed(ctx context.Context, status string, limit int) ([]models.GraphProposedRelation, error) {
	resp, err := c.client.GraphListProposed(c.authCtx(ctx), &gatewaypb.GraphListProposedReq{
		Status: status, Limit: int32(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]models.GraphProposedRelation, 0, len(resp.GetRelations()))
	for _, p := range resp.GetRelations() {
		out = append(out, gwToProposed(p))
	}
	return out, nil
}

func (c *GatewayClient) ApproveProposed(ctx context.Context, id string) (models.GraphProposedRelation, error) {
	p, err := c.client.GraphApproveProposed(c.authCtx(ctx), &gatewaypb.GraphReviewProposedReq{Id: id})
	if err != nil {
		return models.GraphProposedRelation{}, err
	}
	return gwToProposed(p), nil
}

func (c *GatewayClient) RejectProposed(ctx context.Context, id string) (models.GraphProposedRelation, error) {
	p, err := c.client.GraphRejectProposed(c.authCtx(ctx), &gatewaypb.GraphReviewProposedReq{Id: id})
	if err != nil {
		return models.GraphProposedRelation{}, err
	}
	return gwToProposed(p), nil
}

// ---- Verify（介面對齊舊 EmbeddingClient.SearchCount）----

func (c *GatewayClient) SearchCount(ctx context.Context, agentID, query string, topK int) (int, error) {
	if agentID == "" {
		agentID = c.agentID
	}
	resp, err := c.client.VerifySearch(c.authCtx(ctx), &gatewaypb.VerifySearchReq{
		AgentId: agentID, Query: query, TopK: int32(topK),
	})
	if err != nil {
		return 0, err
	}
	return int(resp.GetTotal()), nil
}
