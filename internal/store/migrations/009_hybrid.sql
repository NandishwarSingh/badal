-- Lexical half of hybrid retrieval.
--
-- BM25 lives on the same chunk rows as the vectors rather than on the source
-- tables. One index, one query, one notion of what a "result" is -- and the
-- lexical and semantic halves are then guaranteed to be scoring exactly the
-- same spans of text, which is what makes rank fusion meaningful.
ALTER TABLE embeddings
  ADD COLUMN content_tsv tsvector
  GENERATED ALWAYS AS (to_tsvector('english', content)) STORED;

CREATE INDEX embeddings_fts ON embeddings USING GIN (content_tsv);
