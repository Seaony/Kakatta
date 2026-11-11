ALTER TABLE deliveries ADD COLUMN format_level INTEGER NOT NULL DEFAULT 0 CHECK (format_level IN (0, 1, 2, 3, 4, 5));
