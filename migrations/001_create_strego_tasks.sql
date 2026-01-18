-- strego task queue schema
-- Run this migration to create the necessary tables for PostgreSQL storage

-- Create the tasks table
CREATE TABLE IF NOT EXISTS strego_tasks (
    -- Primary key
    id UUID PRIMARY KEY,
    
    -- Task identification
    type VARCHAR(255) NOT NULL,
    queue VARCHAR(255) NOT NULL DEFAULT 'default',
    
    -- Task state
    state VARCHAR(50) NOT NULL DEFAULT 'pending',
    
    -- Payload (JSON encoded)
    payload JSONB NOT NULL DEFAULT '{}',
    
    -- Error handling
    error TEXT,
    retry_count INT DEFAULT 0,
    max_retry INT DEFAULT 3,
    
    -- Priority (0-10, higher = more important)
    priority INT DEFAULT 0,
    
    -- Timestamps
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    scheduled_at TIMESTAMPTZ,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    
    -- Worker information
    worker_id VARCHAR(255),
    
    -- Tracing
    trace_id VARCHAR(64),
    
    -- Deduplication
    unique_key VARCHAR(255),
    
    -- Custom labels (JSON)
    labels JSONB DEFAULT '{}',
    
    -- State constraint
    CONSTRAINT valid_state CHECK (state IN (
        'pending',
        'scheduled', 
        'active', 
        'completed', 
        'failed', 
        'retry', 
        'dead', 
        'cancelled'
    ))
);

-- Index for queue + state queries (most common)
CREATE INDEX IF NOT EXISTS idx_strego_tasks_queue_state 
    ON strego_tasks(queue, state);

-- Index for active/pending/retry tasks
CREATE INDEX IF NOT EXISTS idx_strego_tasks_state 
    ON strego_tasks(state) 
    WHERE state IN ('pending', 'active', 'retry');

-- Index for task type filtering
CREATE INDEX IF NOT EXISTS idx_strego_tasks_type 
    ON strego_tasks(type);

-- Index for listing tasks by creation time
CREATE INDEX IF NOT EXISTS idx_strego_tasks_created 
    ON strego_tasks(created_at DESC);

-- Index for scheduled tasks
CREATE INDEX IF NOT EXISTS idx_strego_tasks_scheduled 
    ON strego_tasks(scheduled_at) 
    WHERE state = 'scheduled';

-- Unique index for deduplication
CREATE UNIQUE INDEX IF NOT EXISTS idx_strego_tasks_unique 
    ON strego_tasks(unique_key) 
    WHERE unique_key IS NOT NULL;

-- Index for cleanup of old completed/dead tasks
CREATE INDEX IF NOT EXISTS idx_strego_tasks_completed 
    ON strego_tasks(completed_at) 
    WHERE state IN ('completed', 'dead');

-- Index for label-based queries (GIN for JSONB)
CREATE INDEX IF NOT EXISTS idx_strego_tasks_labels 
    ON strego_tasks USING GIN (labels);

-- Comments
COMMENT ON TABLE strego_tasks IS 'strego task queue - stores task history and state';
COMMENT ON COLUMN strego_tasks.id IS 'Unique task identifier (UUID)';
COMMENT ON COLUMN strego_tasks.type IS 'Task type/name used for handler routing';
COMMENT ON COLUMN strego_tasks.queue IS 'Queue name (default, critical, low, etc.)';
COMMENT ON COLUMN strego_tasks.state IS 'Current task state';
COMMENT ON COLUMN strego_tasks.payload IS 'JSON-encoded task payload';
COMMENT ON COLUMN strego_tasks.error IS 'Last error message if task failed';
COMMENT ON COLUMN strego_tasks.retry_count IS 'Number of retry attempts';
COMMENT ON COLUMN strego_tasks.max_retry IS 'Maximum retry attempts allowed';
COMMENT ON COLUMN strego_tasks.priority IS 'Task priority (0-10, higher = more important)';
COMMENT ON COLUMN strego_tasks.unique_key IS 'Unique key for task deduplication';
COMMENT ON COLUMN strego_tasks.labels IS 'Custom labels for filtering and grouping';
