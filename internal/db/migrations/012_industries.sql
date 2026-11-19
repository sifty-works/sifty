-- industries: a JSON array of industry names assigned to each vacancy.
-- Empty array means no industries have been assigned or matched.
ALTER TABLE vacancy ADD COLUMN industries TEXT NOT NULL DEFAULT '[]';
