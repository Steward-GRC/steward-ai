-- Copyright 2026 The Steward Authors
-- SPDX-License-Identifier: Apache-2.0

-- The vector extension stays: other databases on the server may use it.
DROP TABLE IF EXISTS ai_usage_monthly;
DROP TABLE IF EXISTS ai_policy_relationship;
DROP TABLE IF EXISTS ai_related_policies;
DROP TABLE IF EXISTS ai_policy_centroids;
DROP TABLE IF EXISTS ai_user_query_limit;
DROP TABLE IF EXISTS ai_summaries;
DROP TABLE IF EXISTS ai_config;
DROP TABLE IF EXISTS ai_job_results;
DROP TABLE IF EXISTS ai_qa_cache;
DROP TABLE IF EXISTS ai_chunks;
