package workbench

// ImportFile carries bounded metadata only. Header excludes the eight-byte length prefix.
type ImportFile struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Kind    string `json:"kind"`
	Content string `json:"content,omitempty"`
	Header  string `json:"header,omitempty"`
}
type ImportSourceFile struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}
type ImportTensor struct {
	Name  string  `json:"name"`
	Dtype string  `json:"dtype"`
	Shape []int64 `json:"shape"`
	File  string  `json:"file"`
	Bytes int64   `json:"bytes"`
}
type ImportedPosition struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}
type ImportedNode struct {
	ID             string           `json:"id"`
	Kind           string           `json:"kind"`
	Label          string           `json:"label"`
	Position       ImportedPosition `json:"position"`
	Config         map[string]any   `json:"config"`
	TensorNames    []string         `json:"tensorNames"`
	BaselineConfig map[string]any   `json:"baselineConfig"`
	SourceKey      string           `json:"sourceKey,omitempty"`
}
type ImportedBaselineNode struct {
	ID          string         `json:"id"`
	Kind        string         `json:"kind"`
	Config      map[string]any `json:"config"`
	TensorNames []string       `json:"tensorNames"`
	SourceKey   string         `json:"sourceKey,omitempty"`
}
type ImportedEdge struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Target string `json:"target"`
}
type ImportedSource struct {
	Family          string                 `json:"family"`
	Config          map[string]any         `json:"config"`
	Files           []ImportSourceFile     `json:"files"`
	Tensors         []ImportTensor         `json:"tensors"`
	WeightsVerified bool                   `json:"weightsVerified"`
	Warnings        []string               `json:"warnings"`
	BaselineNodes   []ImportedBaselineNode `json:"baselineNodes"`
}
type ImportedGraph struct {
	Version int            `json:"version"`
	Format  string         `json:"format"`
	Source  ImportedSource `json:"source"`
	Nodes   []ImportedNode `json:"nodes"`
	Edges   []ImportedEdge `json:"edges"`
}
type ImportInspection struct {
	Family           string        `json:"family"`
	Name             string        `json:"name"`
	Supported        bool          `json:"supported"`
	WeightsVerified  bool          `json:"weightsVerified"`
	Warnings         []string      `json:"warnings"`
	TensorCount      int           `json:"tensorCount"`
	ParameterCount   int64         `json:"parameterCount"`
	TotalWeightBytes int64         `json:"totalWeightBytes"`
	Graph            ImportedGraph `json:"graph"`
}
