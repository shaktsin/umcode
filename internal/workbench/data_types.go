package workbench

// DataFile contains uploaded bytes, never a server-local filesystem path.
type DataFile struct {
	Name          string `json:"name"`
	ContentBase64 string `json:"contentBase64"`
}

type DataSource struct {
	Kind     string   `json:"kind"`
	Name     string   `json:"name"`
	Repo     string   `json:"repo,omitempty"`
	Revision string   `json:"revision,omitempty"`
	Files    []string `json:"files,omitempty"`
}

type DataColumn struct {
	Name        string   `json:"name"`
	Kind        string   `json:"kind"`
	Missing     int      `json:"missing"`
	UniqueCount int      `json:"uniqueCount"`
	Examples    []string `json:"examples"`
}

type Dataset struct {
	ID        string              `json:"id"`
	Name      string              `json:"name"`
	Source    DataSource          `json:"source"`
	Format    string              `json:"format"`
	RowCount  int                 `json:"rowCount"`
	ByteCount int                 `json:"byteCount"`
	Columns   []DataColumn        `json:"columns"`
	Preview   []map[string]string `json:"preview"`
	CreatedAt string              `json:"createdAt"`
}

type DataConfig struct {
	DatasetID         string   `json:"datasetId"`
	Mode              string   `json:"mode"`
	Purpose           string   `json:"purpose"`
	InputColumns      []string `json:"inputColumns"`
	TargetColumn      string   `json:"targetColumn"`
	Scale             string   `json:"scale"`
	TrainPercent      int      `json:"trainPercent"`
	ValidationPercent int      `json:"validationPercent"`
	TestPercent       int      `json:"testPercent"`
	Seed              int64    `json:"seed"`
	Stratify          bool     `json:"stratify"`
	SequenceLength    int      `json:"sequenceLength"`
	ImageWidth        int      `json:"imageWidth"`
	ImageHeight       int      `json:"imageHeight"`
	ImageChannels     int      `json:"imageChannels"`
}

type DataCounts struct {
	Train      int `json:"train"`
	Validation int `json:"validation"`
	Test       int `json:"test"`
}

type DataStatistic struct {
	Column string  `json:"column"`
	Mean   float64 `json:"mean"`
	Std    float64 `json:"std"`
}

type DataSelection struct {
	Version    string          `json:"version"`
	DatasetID  string          `json:"datasetId"`
	Config     DataConfig      `json:"config"`
	Counts     DataCounts      `json:"counts"`
	Classes    []string        `json:"classes"`
	Statistics []DataStatistic `json:"statistics"`
	CreatedAt  string          `json:"createdAt"`
	Warnings   []string        `json:"warnings"`
}

type DataState struct {
	Datasets  []Dataset      `json:"datasets"`
	Dataset   *Dataset       `json:"dataset"`
	Selection *DataSelection `json:"selection"`
}
