-- A credential is an API key ('api_key') or a ChatGPT account sign-in ('chatgpt').
ALTER TABLE credentials ADD COLUMN kind TEXT NOT NULL DEFAULT 'api_key';
