package models

// 對應後端（agent_graph 服務，預設 :8504）：
//   POST /api/graph/search          （GraphSearchRequest / GraphEntity[]）
//   POST /api/graph/context         （GraphContextRequest / GraphContext）
//   POST /api/graph/hybrid_search   （GraphHybridSearchRequest / GraphHybridSearchResult）

// GraphEntity 一個知識圖譜實體。
type GraphEntity struct {
	Name        string  `json:"name"`
	Category    string  `json:"category,omitempty"`
	Description string  `json:"description,omitempty"`
	SourceFile  string  `json:"source_file,omitempty"`
	Score       float64 `json:"score,omitempty"` // 混合搜尋時填入向量相似度
}

// GraphRelation 兩個實體之間的一條關係。
type GraphRelation struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Type   string `json:"type"`
}

// GraphSearchRequest POST /api/graph/search 請求 body。
type GraphSearchRequest struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

// GraphContextRequest POST /api/graph/context 請求 body。
type GraphContextRequest struct {
	EntityName string `json:"entity_name"`
	Hops       int    `json:"hops,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

// GraphContext 一個實體的區域圖遍歷結果。
type GraphContext struct {
	Center    GraphEntity     `json:"center"`
	Neighbors []GraphEntity   `json:"neighbors"`
	Relations []GraphRelation `json:"relations"`
}

// GraphHybridSearchRequest POST /api/graph/hybrid_search 請求 body。
type GraphHybridSearchRequest struct {
	Query string `json:"query"`
	TopK  int    `json:"top_k,omitempty"`
	Hops  int    `json:"hops,omitempty"`
}

// GraphHybridSearchResult 向量種子 + 各自的圖遍歷結果。
type GraphHybridSearchResult struct {
	Seeds   []GraphEntity  `json:"seeds"`
	Context []GraphContext `json:"context"`
}

// GraphProposedRelation 一筆待人工審核的低信心關係候選
// （對應 agent_graph 的 proposed_relations 表）。
type GraphProposedRelation struct {
	ID           string  `json:"id"`
	SourceEntity string  `json:"source_entity"`
	TargetEntity string  `json:"target_entity"`
	RelationType string  `json:"relation_type"`
	Confidence   float64 `json:"confidence"`
	SourceFile   string  `json:"source_file,omitempty"`
	ExtractedBy  string  `json:"extracted_by,omitempty"`
	Status       string  `json:"status"`
	CreatedAt    string  `json:"created_at"`
}

// GraphListProposedRequest POST /api/graph/proposed/list 請求 body。
type GraphListProposedRequest struct {
	Status string `json:"status,omitempty"` // 空 = pending
	Limit  int    `json:"limit,omitempty"`
}

// GraphReviewProposedRequest POST /api/graph/proposed/{approve,reject} 請求 body。
type GraphReviewProposedRequest struct {
	ID string `json:"id"`
}
