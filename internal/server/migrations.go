package server

import (
	"database/sql"
	"errors"
)

func migrate(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version > 1 {
		return errors.New("database schema is newer than this Fileament build")
	}
	if version == 0 {
		if _, err := tx.Exec(schema); err != nil {
			return err
		}
		if _, err := tx.Exec(`PRAGMA user_version = 1`); err != nil {
			return err
		}
	}
	return tx.Commit()
}

const schema = `
CREATE TABLE IF NOT EXISTS models (
  id TEXT PRIMARY KEY,
  title TEXT NOT NULL,
  description TEXT DEFAULT '',
  source_url TEXT,
  license TEXT,
  author TEXT,
  primary_thumb TEXT,
  total_bytes INTEGER DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS files (
  id TEXT PRIMARY KEY,
  model_id TEXT NOT NULL REFERENCES models(id) ON DELETE CASCADE,
  filename TEXT NOT NULL,
  rel_path TEXT NOT NULL,
  format TEXT NOT NULL,
  size_bytes INTEGER NOT NULL,
  sha256 TEXT,
  triangle_count INTEGER,
  bbox_x REAL, bbox_y REAL, bbox_z REAL,
  thumb_path TEXT,
  sort_order INTEGER DEFAULT 0
);
CREATE TABLE IF NOT EXISTS images (
  id TEXT PRIMARY KEY,
  model_id TEXT NOT NULL REFERENCES models(id) ON DELETE CASCADE,
  rel_path TEXT NOT NULL,
  sort_order INTEGER DEFAULT 0
);
CREATE TABLE IF NOT EXISTS collections (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  slug TEXT NOT NULL UNIQUE,
  description TEXT DEFAULT '',
  cover_model_id TEXT REFERENCES models(id) ON DELETE SET NULL,
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS collection_models (
  collection_id TEXT NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
  model_id TEXT NOT NULL REFERENCES models(id) ON DELETE CASCADE,
  sort_order INTEGER DEFAULT 0,
  PRIMARY KEY (collection_id, model_id)
);
CREATE TABLE IF NOT EXISTS tags (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  slug TEXT NOT NULL UNIQUE
);
CREATE TABLE IF NOT EXISTS model_tags (
  model_id TEXT NOT NULL REFERENCES models(id) ON DELETE CASCADE,
  tag_id TEXT NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
  PRIMARY KEY (model_id, tag_id)
);
CREATE TABLE IF NOT EXISTS share_links (
  id TEXT PRIMARY KEY,
  token TEXT NOT NULL UNIQUE,
  scope TEXT NOT NULL,
  target_id TEXT NOT NULL,
  label TEXT,
  expires_at INTEGER,
  revoked_at INTEGER,
  hit_count INTEGER DEFAULT 0,
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
  token TEXT PRIMARY KEY,
  expires_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS jobs (
  id TEXT PRIMARY KEY,
  type TEXT NOT NULL,
  file_id TEXT,
  status TEXT NOT NULL,
  attempts INTEGER DEFAULT 0,
  error TEXT,
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE VIRTUAL TABLE IF NOT EXISTS models_fts USING fts5(title, description, tags, content='', contentless_delete=1, tokenize='porter unicode61');
CREATE TRIGGER IF NOT EXISTS models_ai AFTER INSERT ON models BEGIN
  INSERT INTO models_fts(rowid, title, description, tags) VALUES (new.rowid, new.title, new.description, '');
END;
CREATE TRIGGER IF NOT EXISTS models_au AFTER UPDATE OF title, description ON models BEGIN
  DELETE FROM models_fts WHERE rowid = old.rowid;
  INSERT INTO models_fts(rowid, title, description, tags)
  VALUES (new.rowid, new.title, new.description, COALESCE((SELECT group_concat(tags.name, ' ') FROM tags JOIN model_tags ON tags.id = model_tags.tag_id WHERE model_tags.model_id = new.id), ''));
END;
CREATE TRIGGER IF NOT EXISTS models_ad AFTER DELETE ON models BEGIN
  DELETE FROM models_fts WHERE rowid = old.rowid;
END;
CREATE TRIGGER IF NOT EXISTS model_tags_ai AFTER INSERT ON model_tags BEGIN
  DELETE FROM models_fts WHERE rowid = (SELECT rowid FROM models WHERE id = new.model_id);
  INSERT INTO models_fts(rowid, title, description, tags)
  SELECT rowid, title, description, COALESCE((SELECT group_concat(tags.name, ' ') FROM tags JOIN model_tags ON tags.id = model_tags.tag_id WHERE model_tags.model_id = new.model_id), '')
  FROM models WHERE id = new.model_id;
END;
CREATE TRIGGER IF NOT EXISTS model_tags_ad AFTER DELETE ON model_tags BEGIN
  DELETE FROM models_fts WHERE rowid = (SELECT rowid FROM models WHERE id = old.model_id);
  INSERT INTO models_fts(rowid, title, description, tags)
  SELECT rowid, title, description, COALESCE((SELECT group_concat(tags.name, ' ') FROM tags JOIN model_tags ON tags.id = model_tags.tag_id WHERE model_tags.model_id = old.model_id), '')
  FROM models WHERE id = old.model_id;
END;
`
