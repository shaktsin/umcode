CREATE TABLE model_capacity_profiles (
 provider TEXT NOT NULL,
 endpoint_id TEXT NOT NULL,
 model TEXT NOT NULL,
 data_json TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 PRIMARY KEY(provider,endpoint_id,model)
);
