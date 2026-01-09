-- Optional: Create a view for task statistics
-- This provides a convenient way to query overall stats

CREATE OR REPLACE VIEW strego_task_stats AS
SELECT 
    COUNT(*) as total_tasks,
    COUNT(*) FILTER (WHERE state = 'pending') as pending_tasks,
    COUNT(*) FILTER (WHERE state = 'scheduled') as scheduled_tasks,
    COUNT(*) FILTER (WHERE state = 'active') as active_tasks,
    COUNT(*) FILTER (WHERE state = 'completed') as completed_tasks,
    COUNT(*) FILTER (WHERE state = 'failed') as failed_tasks,
    COUNT(*) FILTER (WHERE state = 'retry') as retry_tasks,
    COUNT(*) FILTER (WHERE state = 'dead') as dead_tasks,
    COUNT(*) FILTER (WHERE state = 'cancelled') as cancelled_tasks
FROM strego_tasks;

-- Create a view for per-queue statistics
CREATE OR REPLACE VIEW strego_queue_stats AS
SELECT 
    queue,
    COUNT(*) as total_tasks,
    COUNT(*) FILTER (WHERE state = 'pending') as pending_tasks,
    COUNT(*) FILTER (WHERE state = 'active') as active_tasks,
    COUNT(*) FILTER (WHERE state = 'completed') as completed_tasks,
    COUNT(*) FILTER (WHERE state = 'failed') as failed_tasks,
    COUNT(*) FILTER (WHERE state = 'dead') as dead_tasks,
    AVG(EXTRACT(EPOCH FROM (completed_at - started_at))) 
        FILTER (WHERE completed_at IS NOT NULL AND started_at IS NOT NULL) as avg_duration_seconds,
    MAX(created_at) as last_task_at
FROM strego_tasks
GROUP BY queue;

-- Create a view for task type statistics
CREATE OR REPLACE VIEW strego_type_stats AS
SELECT 
    type,
    COUNT(*) as total_tasks,
    COUNT(*) FILTER (WHERE state = 'completed') as completed_tasks,
    COUNT(*) FILTER (WHERE state = 'failed' OR state = 'dead') as failed_tasks,
    AVG(retry_count) FILTER (WHERE state = 'completed') as avg_retries,
    AVG(EXTRACT(EPOCH FROM (completed_at - started_at))) 
        FILTER (WHERE completed_at IS NOT NULL AND started_at IS NOT NULL) as avg_duration_seconds
FROM strego_tasks
GROUP BY type;

COMMENT ON VIEW strego_task_stats IS 'Overall task statistics';
COMMENT ON VIEW strego_queue_stats IS 'Per-queue task statistics';
COMMENT ON VIEW strego_type_stats IS 'Per-task-type statistics';
