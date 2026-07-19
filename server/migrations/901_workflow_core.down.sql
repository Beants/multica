-- Drop the workflow engine core schema. Reverse dependency order. Indexes
-- live in their own migrations (902-925) and are dropped by their down
-- files before this one runs; dropping the tables clears anything left.
DROP TABLE IF EXISTS step_transition;
DROP TABLE IF EXISTS acceptance;
DROP TABLE IF EXISTS verdict;
DROP TABLE IF EXISTS submission;
DROP TABLE IF EXISTS step_instance;
DROP TABLE IF EXISTS workflow_run;
DROP TABLE IF EXISTS workflow_hook;
DROP TABLE IF EXISTS workflow_edge;
DROP TABLE IF EXISTS workflow_node;
DROP TABLE IF EXISTS workflow_template;
