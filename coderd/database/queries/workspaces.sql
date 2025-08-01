-- name: GetWorkspaceByID :one
SELECT
	*
FROM
	workspaces_expanded
WHERE
	id = $1
LIMIT
	1;

-- name: GetWorkspaceByResourceID :one
SELECT
	*
FROM
	workspaces_expanded as workspaces
WHERE
	workspaces.id = (
		SELECT
			workspace_id
		FROM
			workspace_builds
		WHERE
			workspace_builds.job_id = (
				SELECT
					job_id
				FROM
					workspace_resources
				WHERE
					workspace_resources.id = @resource_id
			)
	)
LIMIT
	1;

-- name: GetWorkspaceByWorkspaceAppID :one
SELECT
	*
FROM
	workspaces_expanded as workspaces
WHERE
		workspaces.id = (
		SELECT
			workspace_id
		FROM
			workspace_builds
		WHERE
				workspace_builds.job_id = (
				SELECT
					job_id
				FROM
					workspace_resources
				WHERE
						workspace_resources.id = (
						SELECT
							resource_id
						FROM
							workspace_agents
						WHERE
								workspace_agents.id = (
								SELECT
									agent_id
								FROM
									workspace_apps
								WHERE
									workspace_apps.id = @workspace_app_id
								)
					)
			)
	);

-- name: GetWorkspaceByAgentID :one
SELECT
	*
FROM
	workspaces_expanded as workspaces
WHERE
	workspaces.id = (
		SELECT
			workspace_id
		FROM
			workspace_builds
		WHERE
			workspace_builds.job_id = (
				SELECT
					job_id
				FROM
					workspace_resources
				WHERE
					workspace_resources.id = (
						SELECT
							resource_id
						FROM
							workspace_agents
						WHERE
							workspace_agents.id = @agent_id
					)
			)
	);

-- name: GetWorkspaces :many
WITH
-- build_params is used to filter by build parameters if present.
-- It has to be a CTE because the set returning function 'unnest' cannot
-- be used in a WHERE clause.
build_params AS (
SELECT
	LOWER(unnest(@param_names :: text[])) AS name,
	LOWER(unnest(@param_values :: text[])) AS value
),
filtered_workspaces AS (
SELECT
	workspaces.*,
	latest_build.template_version_id,
	latest_build.template_version_name,
	latest_build.completed_at as latest_build_completed_at,
	latest_build.canceled_at as latest_build_canceled_at,
	latest_build.error as latest_build_error,
	latest_build.transition as latest_build_transition,
	latest_build.job_status as latest_build_status,
	latest_build.has_ai_task as latest_build_has_ai_task
FROM
	workspaces_expanded as workspaces
JOIN
    users
ON
    workspaces.owner_id = users.id
LEFT JOIN LATERAL (
	SELECT
		workspace_builds.id,
		workspace_builds.transition,
		workspace_builds.template_version_id,
		workspace_builds.has_ai_task,
		template_versions.name AS template_version_name,
		provisioner_jobs.id AS provisioner_job_id,
		provisioner_jobs.started_at,
		provisioner_jobs.updated_at,
		provisioner_jobs.canceled_at,
		provisioner_jobs.completed_at,
		provisioner_jobs.error,
		provisioner_jobs.job_status
	FROM
		workspace_builds
	JOIN
		provisioner_jobs
	ON
		provisioner_jobs.id = workspace_builds.job_id
	LEFT JOIN
		template_versions
	ON
		template_versions.id = workspace_builds.template_version_id
	WHERE
		workspace_builds.workspace_id = workspaces.id
	ORDER BY
		build_number DESC
	LIMIT
		1
) latest_build ON TRUE
LEFT JOIN LATERAL (
	SELECT
		*
	FROM
		templates
	WHERE
		templates.id = workspaces.template_id
) template ON true
WHERE
	-- Optionally include deleted workspaces
	workspaces.deleted = @deleted
	AND CASE
		WHEN @status :: text != '' THEN
			CASE
			    -- Some workspace specific status refer to the transition
			    -- type. By default, the standard provisioner job status
			    -- search strings are supported.
			    -- 'running' states
				WHEN @status = 'starting' THEN
				    latest_build.job_status = 'running'::provisioner_job_status AND
					latest_build.transition = 'start'::workspace_transition
				WHEN @status = 'stopping' THEN
					latest_build.job_status = 'running'::provisioner_job_status AND
					latest_build.transition = 'stop'::workspace_transition
				WHEN @status = 'deleting' THEN
					latest_build.job_status = 'running' AND
					latest_build.transition = 'delete'::workspace_transition

			    -- 'succeeded' states
			    WHEN @status = 'deleted' THEN
			    	latest_build.job_status = 'succeeded'::provisioner_job_status AND
			    	latest_build.transition = 'delete'::workspace_transition
				WHEN @status = 'stopped' THEN
					latest_build.job_status = 'succeeded'::provisioner_job_status AND
					latest_build.transition = 'stop'::workspace_transition
				WHEN @status = 'started' THEN
					latest_build.job_status = 'succeeded'::provisioner_job_status AND
					latest_build.transition = 'start'::workspace_transition

			    -- Special case where the provisioner status and workspace status
			    -- differ. A workspace is "running" if the job is "succeeded" and
			    -- the transition is "start". This is because a workspace starts
			    -- running when a job is complete.
			    WHEN @status = 'running' THEN
					latest_build.job_status = 'succeeded'::provisioner_job_status AND
					latest_build.transition = 'start'::workspace_transition

				WHEN @status != '' THEN
				    -- By default just match the job status exactly
			    	latest_build.job_status = @status::provisioner_job_status
				ELSE
					true
			END
		ELSE true
	END
	-- Filter by owner_id
	AND CASE
		WHEN @owner_id :: uuid != '00000000-0000-0000-0000-000000000000'::uuid THEN
			workspaces.owner_id = @owner_id
		ELSE true
	END
  	-- Filter by organization_id
  	AND CASE
		  WHEN @organization_id :: uuid != '00000000-0000-0000-0000-000000000000'::uuid THEN
			  workspaces.organization_id = @organization_id
		  ELSE true
	END
	-- Filter by build parameter
   	-- @has_param will match any build that includes the parameter.
	AND CASE WHEN array_length(@has_param :: text[], 1) > 0  THEN
		EXISTS (
			SELECT
				1
			FROM
				workspace_build_parameters
			WHERE
				workspace_build_parameters.workspace_build_id = latest_build.id AND
				-- ILIKE is case insensitive
				workspace_build_parameters.name ILIKE ANY(@has_param)
		)
		ELSE true
	END
	-- @param_value will match param name an value.
  	-- requires 2 arrays, @param_names and @param_values to be passed in.
  	-- Array index must match between the 2 arrays for name=value
  	AND CASE WHEN array_length(@param_names :: text[], 1) > 0  THEN
		EXISTS (
			SELECT
				1
			FROM
				workspace_build_parameters
			INNER JOIN
				build_params
			ON
				LOWER(workspace_build_parameters.name) = build_params.name AND
				LOWER(workspace_build_parameters.value) = build_params.value AND
				workspace_build_parameters.workspace_build_id = latest_build.id
		)
		ELSE true
	END

	-- Filter by owner_name
	AND CASE
		WHEN @owner_username :: text != '' THEN
			workspaces.owner_id = (SELECT id FROM users WHERE lower(users.username) = lower(@owner_username) AND deleted = false)
		ELSE true
	END
	-- Filter by template_name
	-- There can be more than 1 template with the same name across organizations.
	-- Use the organization filter to restrict to 1 org if needed.
	AND CASE
		WHEN @template_name :: text != '' THEN
			workspaces.template_id = ANY(SELECT id FROM templates WHERE lower(name) = lower(@template_name) AND deleted = false)
		ELSE true
	END
	-- Filter by template_ids
	AND CASE
		WHEN array_length(@template_ids :: uuid[], 1) > 0 THEN
			workspaces.template_id = ANY(@template_ids)
		ELSE true
	END
  	-- Filter by workspace_ids
  	AND CASE
		  WHEN array_length(@workspace_ids :: uuid[], 1) > 0 THEN
			  workspaces.id = ANY(@workspace_ids)
		  ELSE true
	END
	-- Filter by name, matching on substring
	AND CASE
		WHEN @name :: text != '' THEN
			workspaces.name ILIKE '%' || @name || '%'
		ELSE true
	END
	-- Filter by agent status
	-- has-agent: is only applicable for workspaces in "start" transition. Stopped and deleted workspaces don't have agents.
	AND CASE
		WHEN @has_agent :: text != '' THEN
			(
				SELECT COUNT(*)
				FROM
					workspace_resources
				JOIN
					workspace_agents
				ON
					workspace_agents.resource_id = workspace_resources.id
				WHERE
					workspace_resources.job_id = latest_build.provisioner_job_id AND
					latest_build.transition = 'start'::workspace_transition AND
					-- Filter out deleted sub agents.
					workspace_agents.deleted = FALSE AND
					@has_agent = (
						CASE
							WHEN workspace_agents.first_connected_at IS NULL THEN
								CASE
									WHEN workspace_agents.connection_timeout_seconds > 0 AND NOW() - workspace_agents.created_at > workspace_agents.connection_timeout_seconds * INTERVAL '1 second' THEN
										'timeout'
									ELSE
										'connecting'
								END
							WHEN workspace_agents.disconnected_at > workspace_agents.last_connected_at THEN
								'disconnected'
							WHEN NOW() - workspace_agents.last_connected_at > INTERVAL '1 second' * @agent_inactive_disconnect_timeout_seconds :: bigint THEN
								'disconnected'
							WHEN workspace_agents.last_connected_at IS NOT NULL THEN
								'connected'
							ELSE
								NULL
						END
					)
			) > 0
		ELSE true
	END
	-- Filter by dormant workspaces.
	AND CASE
		WHEN @dormant :: boolean != 'false' THEN
			dormant_at IS NOT NULL
		ELSE true
	END
	-- Filter by last_used
	AND CASE
		  WHEN @last_used_before :: timestamp with time zone > '0001-01-01 00:00:00Z' THEN
				  workspaces.last_used_at <= @last_used_before
		  ELSE true
	END
	AND CASE
		  WHEN @last_used_after :: timestamp with time zone > '0001-01-01 00:00:00Z' THEN
				  workspaces.last_used_at >= @last_used_after
		  ELSE true
	END
  	AND CASE
		  WHEN sqlc.narg('using_active') :: boolean IS NOT NULL THEN
			  (latest_build.template_version_id = template.active_version_id) = sqlc.narg('using_active') :: boolean
		  ELSE true
	END
	-- Filter by has_ai_task in latest build
	AND CASE
		WHEN sqlc.narg('has_ai_task') :: boolean IS NOT NULL THEN
			(COALESCE(latest_build.has_ai_task, false) OR (
				-- If the build has no AI task, it means that the provisioner job is in progress
				-- and we don't know if it has an AI task yet. In this case, we optimistically
				-- assume that it has an AI task if the AI Prompt parameter is not empty. This
				-- lets the AI Task frontend spawn a task and see it immediately after instead of
				-- having to wait for the build to complete.
				latest_build.has_ai_task IS NULL AND
				latest_build.completed_at IS NULL AND
				EXISTS (
					SELECT 1
					FROM workspace_build_parameters
					WHERE workspace_build_parameters.workspace_build_id = latest_build.id
					AND workspace_build_parameters.name = 'AI Prompt'
					AND workspace_build_parameters.value != ''
				)
			)) = (sqlc.narg('has_ai_task') :: boolean)
		ELSE true
	END
	-- Authorize Filter clause will be injected below in GetAuthorizedWorkspaces
	-- @authorize_filter
), filtered_workspaces_order AS (
	SELECT
		fw.*
	FROM
		filtered_workspaces fw
	ORDER BY
		-- To ensure that 'favorite' workspaces show up first in the list only for their owner.
		CASE WHEN owner_id = @requester_id AND favorite THEN 0 ELSE 1 END ASC,
		(latest_build_completed_at IS NOT NULL AND
			latest_build_canceled_at IS NULL AND
			latest_build_error IS NULL AND
			latest_build_transition = 'start'::workspace_transition) DESC,
		LOWER(owner_username) ASC,
		LOWER(name) ASC
	LIMIT
		CASE
			WHEN @limit_ :: integer > 0 THEN
				@limit_
		END
	OFFSET
		@offset_
), filtered_workspaces_order_with_summary AS (
	SELECT
		fwo.*
	FROM
		filtered_workspaces_order fwo
	-- Return a technical summary row with total count of workspaces.
	-- It is used to present the correct count if pagination goes beyond the offset.
	UNION ALL
	SELECT
		'00000000-0000-0000-0000-000000000000'::uuid, -- id
		'0001-01-01 00:00:00+00'::timestamptz, -- created_at
		'0001-01-01 00:00:00+00'::timestamptz, -- updated_at
		'00000000-0000-0000-0000-000000000000'::uuid, -- owner_id
		'00000000-0000-0000-0000-000000000000'::uuid, -- organization_id
		'00000000-0000-0000-0000-000000000000'::uuid, -- template_id
		false, -- deleted
		'**TECHNICAL_ROW**', -- name
		'', -- autostart_schedule
		0, -- ttl
		'0001-01-01 00:00:00+00'::timestamptz, -- last_used_at
		'0001-01-01 00:00:00+00'::timestamptz, -- dormant_at
		'0001-01-01 00:00:00+00'::timestamptz, -- deleting_at
		'never'::automatic_updates, -- automatic_updates
		false, -- favorite
		'0001-01-01 00:00:00+00'::timestamptz, -- next_start_at
		'{}'::jsonb, -- group_acl
		'{}'::jsonb, -- user_acl
		'', -- owner_avatar_url
		'', -- owner_username
		'', -- owner_name
		'', -- organization_name
		'', -- organization_display_name
		'', -- organization_icon
		'', -- organization_description
		'', -- template_name
		'', -- template_display_name
		'', -- template_icon
		'', -- template_description
		-- Extra columns added to `filtered_workspaces`
		'00000000-0000-0000-0000-000000000000'::uuid, -- template_version_id
		'', -- template_version_name
		'0001-01-01 00:00:00+00'::timestamptz, -- latest_build_completed_at,
		'0001-01-01 00:00:00+00'::timestamptz, -- latest_build_canceled_at,
		'', -- latest_build_error
		'start'::workspace_transition, -- latest_build_transition
		'unknown'::provisioner_job_status, -- latest_build_status
		false -- latest_build_has_ai_task
	WHERE
		@with_summary :: boolean = true
), total_count AS (
	SELECT
		count(*) AS count
    FROM
		filtered_workspaces
)
SELECT
	fwos.*,
	tc.count
FROM
	filtered_workspaces_order_with_summary fwos
CROSS JOIN
	total_count tc;

-- name: GetWorkspaceByOwnerIDAndName :one
SELECT
	*
FROM
	workspaces_expanded as workspaces
WHERE
	owner_id = @owner_id
	AND deleted = @deleted
	AND LOWER("name") = LOWER(@name)
ORDER BY created_at DESC;

-- name: GetWorkspaceUniqueOwnerCountByTemplateIDs :many
SELECT templates.id AS template_id, COUNT(DISTINCT workspaces.owner_id) AS unique_owners_sum
FROM templates
LEFT JOIN workspaces ON workspaces.template_id = templates.id AND workspaces.deleted = false
WHERE templates.id = ANY(@template_ids :: uuid[])
GROUP BY templates.id;

-- name: InsertWorkspace :one
INSERT INTO
	workspaces (
		id,
		created_at,
		updated_at,
		owner_id,
		organization_id,
		template_id,
		name,
		autostart_schedule,
		ttl,
		last_used_at,
		automatic_updates,
		next_start_at
	)
VALUES
	($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) RETURNING *;

-- name: UpdateWorkspaceDeletedByID :exec
UPDATE
	workspaces
SET
	deleted = $2
WHERE
	id = $1;

-- name: UpdateWorkspace :one
UPDATE
	workspaces
SET
	name = $2
WHERE
	id = $1
	AND deleted = false
RETURNING *;

-- name: UpdateWorkspaceAutostart :exec
UPDATE
	workspaces
SET
	autostart_schedule = $2,
	next_start_at = $3
WHERE
	id = $1;

-- name: UpdateWorkspaceNextStartAt :exec
UPDATE
	workspaces
SET
	next_start_at = $2
WHERE
	id = $1;

-- name: BatchUpdateWorkspaceNextStartAt :exec
UPDATE
	workspaces
SET
	next_start_at = CASE
		WHEN batch.next_start_at = '0001-01-01 00:00:00+00'::timestamptz THEN NULL
		ELSE batch.next_start_at
	END
FROM (
	SELECT
		unnest(sqlc.arg(ids)::uuid[]) AS id,
		unnest(sqlc.arg(next_start_ats)::timestamptz[]) AS next_start_at
) AS batch
WHERE
	workspaces.id = batch.id;

-- name: UpdateWorkspaceTTL :exec
UPDATE
	workspaces
SET
	ttl = $2
WHERE
	id = $1;

-- name: UpdateWorkspacesTTLByTemplateID :exec
UPDATE
		workspaces
SET
		ttl = $2
WHERE
		template_id = $1;

-- name: UpdateWorkspaceLastUsedAt :exec
UPDATE
	workspaces
SET
	last_used_at = $2
WHERE
	id = $1;

-- name: BatchUpdateWorkspaceLastUsedAt :exec
UPDATE
	workspaces
SET
	last_used_at = @last_used_at
WHERE
	id = ANY(@ids :: uuid[])
AND
  -- Do not overwrite with older data
  last_used_at < @last_used_at;

-- name: GetDeploymentWorkspaceStats :one
WITH workspaces_with_jobs AS (
	SELECT
	latest_build.* FROM workspaces
	LEFT JOIN LATERAL (
		SELECT
			workspace_builds.transition,
			provisioner_jobs.id AS provisioner_job_id,
			provisioner_jobs.started_at,
			provisioner_jobs.updated_at,
			provisioner_jobs.canceled_at,
			provisioner_jobs.completed_at,
			provisioner_jobs.error
		FROM
			workspace_builds
		LEFT JOIN
			provisioner_jobs
		ON
			provisioner_jobs.id = workspace_builds.job_id
		WHERE
			workspace_builds.workspace_id = workspaces.id
		ORDER BY
			build_number DESC
		LIMIT
			1
	) latest_build ON TRUE WHERE deleted = false
), pending_workspaces AS (
	SELECT COUNT(*) AS count FROM workspaces_with_jobs WHERE
		started_at IS NULL
), building_workspaces AS (
	SELECT COUNT(*) AS count FROM workspaces_with_jobs WHERE
		started_at IS NOT NULL AND
		canceled_at IS NULL AND
		completed_at IS NULL AND
		updated_at - INTERVAL '30 seconds' < NOW()
), running_workspaces AS (
	SELECT COUNT(*) AS count FROM workspaces_with_jobs WHERE
		completed_at IS NOT NULL AND
		canceled_at IS NULL AND
		error IS NULL AND
		transition = 'start'::workspace_transition
), failed_workspaces AS (
	SELECT COUNT(*) AS count FROM workspaces_with_jobs WHERE
		(canceled_at IS NOT NULL AND
			error IS NOT NULL) OR
		(completed_at IS NOT NULL AND
			error IS NOT NULL)
), stopped_workspaces AS (
	SELECT COUNT(*) AS count FROM workspaces_with_jobs WHERE
		completed_at IS NOT NULL AND
		canceled_at IS NULL AND
		error IS NULL AND
		transition = 'stop'::workspace_transition
)
SELECT
	pending_workspaces.count AS pending_workspaces,
	building_workspaces.count AS building_workspaces,
	running_workspaces.count AS running_workspaces,
	failed_workspaces.count AS failed_workspaces,
	stopped_workspaces.count AS stopped_workspaces
FROM pending_workspaces, building_workspaces, running_workspaces, failed_workspaces, stopped_workspaces;

-- name: GetWorkspacesEligibleForTransition :many
SELECT
	workspaces.id,
	workspaces.name,
	workspace_builds.template_version_id as build_template_version_id
FROM
	workspaces
LEFT JOIN
	workspace_builds ON workspace_builds.workspace_id = workspaces.id
INNER JOIN
	provisioner_jobs ON workspace_builds.job_id = provisioner_jobs.id
INNER JOIN
	templates ON workspaces.template_id = templates.id
INNER JOIN
	users ON workspaces.owner_id = users.id
WHERE
	workspace_builds.build_number = (
		SELECT
			MAX(build_number)
		FROM
			workspace_builds
		WHERE
			workspace_builds.workspace_id = workspaces.id
	) AND

	(
		-- A workspace may be eligible for autostop if the following are true:
		--   * The provisioner job has not failed.
		--   * The workspace is not dormant.
		--   * The workspace build was a start transition.
		--   * The workspace's owner is suspended OR the workspace build deadline has passed.
		(
			provisioner_jobs.job_status != 'failed'::provisioner_job_status AND
			workspaces.dormant_at IS NULL AND
			workspace_builds.transition = 'start'::workspace_transition AND (
				users.status = 'suspended'::user_status OR (
					workspace_builds.deadline != '0001-01-01 00:00:00+00'::timestamptz AND
					workspace_builds.deadline < @now :: timestamptz
				)
			)
		) OR

		-- A workspace may be eligible for autostart if the following are true:
		--   * The workspace's owner is active.
		--   * The provisioner job did not fail.
		--   * The workspace build was a stop transition.
		--   * The workspace is not dormant
		--   * The workspace has an autostart schedule.
		--   * It is after the workspace's next start time.
		(
			users.status = 'active'::user_status AND
			provisioner_jobs.job_status != 'failed'::provisioner_job_status AND
			workspace_builds.transition = 'stop'::workspace_transition AND
			workspaces.dormant_at IS NULL AND
			workspaces.autostart_schedule IS NOT NULL AND
			(
				-- next_start_at might be null in these two scenarios:
				--   * A coder instance was updated and we haven't updated next_start_at yet.
				--   * A database trigger made it null because of an update to a related column.
				--
				-- When this occurs, we return the workspace so the Coder server can
				-- compute a valid next start at and update it.
				workspaces.next_start_at IS NULL OR
				workspaces.next_start_at <= @now :: timestamptz
			)
		) OR

		-- A workspace may be eligible for dormant stop if the following are true:
		--   * The workspace is not dormant.
		--   * The template has set a time 'til dormant.
		--   * The workspace has been unused for longer than the time 'til dormancy.
		(
			workspaces.dormant_at IS NULL AND
			templates.time_til_dormant > 0 AND
			(@now :: timestamptz) - workspaces.last_used_at > (INTERVAL '1 millisecond' * (templates.time_til_dormant / 1000000))
		) OR

		-- A workspace may be eligible for deletion if the following are true:
		--   * The workspace is dormant.
		--   * The workspace is scheduled to be deleted.
		--   * If there was a prior attempt to delete the workspace that failed:
		--      * This attempt was at least 24 hours ago.
		(
			workspaces.dormant_at IS NOT NULL AND
			workspaces.deleting_at IS NOT NULL AND
			workspaces.deleting_at < @now :: timestamptz AND
			templates.time_til_dormant_autodelete > 0 AND
			CASE
				WHEN (
					workspace_builds.transition = 'delete'::workspace_transition AND
					provisioner_jobs.job_status = 'failed'::provisioner_job_status
				) THEN (
					(
						provisioner_jobs.canceled_at IS NOT NULL OR
						provisioner_jobs.completed_at IS NOT NULL
					) AND (
						(@now :: timestamptz) - (CASE
							WHEN provisioner_jobs.canceled_at IS NOT NULL THEN provisioner_jobs.canceled_at
							ELSE provisioner_jobs.completed_at
						END) > INTERVAL '24 hours'
					)
				)
				ELSE true
			END
		) OR

		-- A workspace may be eligible for failed stop if the following are true:
		--   * The template has a failure ttl set.
		--   * The workspace build was a start transition.
		--   * The provisioner job failed.
		--   * The provisioner job had completed.
		--   * The provisioner job has been completed for longer than the failure ttl.
		(
			templates.failure_ttl > 0 AND
			workspace_builds.transition = 'start'::workspace_transition AND
			provisioner_jobs.job_status = 'failed'::provisioner_job_status AND
			provisioner_jobs.completed_at IS NOT NULL AND
			(@now :: timestamptz) - provisioner_jobs.completed_at > (INTERVAL '1 millisecond' * (templates.failure_ttl / 1000000))
		)
	)
  	AND workspaces.deleted = 'false'
  	-- Prebuilt workspaces (identified by having the prebuilds system user as owner_id)
	-- should not be considered by the lifecycle executor, as they are handled by the
	-- prebuilds reconciliation loop.
  	AND workspaces.owner_id != 'c42fdf75-3097-471c-8c33-fb52454d81c0'::UUID;

-- name: UpdateWorkspaceDormantDeletingAt :one
UPDATE
    workspaces
SET
    dormant_at = $2,
    -- When a workspace is active we want to update the last_used_at to avoid the workspace going
    -- immediately dormant. If we're transition the workspace to dormant then we leave it alone.
    last_used_at = CASE WHEN $2::timestamptz IS NULL THEN
        now() at time zone 'utc'
    ELSE
        last_used_at
    END,
    -- If dormant_at is null (meaning active) or the template-defined time_til_dormant_autodelete is 0 we should set
    -- deleting_at to NULL else set it to the dormant_at + time_til_dormant_autodelete duration.
    deleting_at = CASE WHEN $2::timestamptz IS NULL OR templates.time_til_dormant_autodelete = 0 THEN
        NULL
    ELSE
        $2::timestamptz + (INTERVAL '1 millisecond' * (templates.time_til_dormant_autodelete / 1000000))
    END
FROM
    templates
WHERE
    workspaces.id = $1
    AND templates.id = workspaces.template_id
RETURNING
    workspaces.*;

-- name: UpdateWorkspacesDormantDeletingAtByTemplateID :many
UPDATE workspaces
SET
    deleting_at = CASE
        WHEN @time_til_dormant_autodelete_ms::bigint = 0 THEN NULL
        WHEN @dormant_at::timestamptz > '0001-01-01 00:00:00+00'::timestamptz THEN  (@dormant_at::timestamptz) + interval '1 milliseconds' * @time_til_dormant_autodelete_ms::bigint
        ELSE dormant_at + interval '1 milliseconds' * @time_til_dormant_autodelete_ms::bigint
    END,
    dormant_at = CASE WHEN @dormant_at::timestamptz > '0001-01-01 00:00:00+00'::timestamptz THEN @dormant_at::timestamptz ELSE dormant_at END
WHERE
    template_id = @template_id
AND
    dormant_at IS NOT NULL
RETURNING *;

-- name: UpdateTemplateWorkspacesLastUsedAt :exec
UPDATE workspaces
SET
	last_used_at = @last_used_at::timestamptz
WHERE
	template_id = @template_id;

-- name: UpdateWorkspaceAutomaticUpdates :exec
UPDATE
	workspaces
SET
	automatic_updates = $2
WHERE
		id = $1;

-- name: FavoriteWorkspace :exec
UPDATE workspaces SET favorite = true WHERE id = @id;

-- name: UnfavoriteWorkspace :exec
UPDATE workspaces SET favorite = false WHERE id = @id;

-- name: GetWorkspacesAndAgentsByOwnerID :many
SELECT
	workspaces.id as id,
	workspaces.name as name,
	job_status,
	transition,
	(array_agg(ROW(agent_id, agent_name)::agent_id_name_pair) FILTER (WHERE agent_id IS NOT NULL))::agent_id_name_pair[] as agents
FROM workspaces
LEFT JOIN LATERAL (
	SELECT
		workspace_id,
		job_id,
		transition,
		job_status
	FROM workspace_builds
	JOIN provisioner_jobs ON provisioner_jobs.id = workspace_builds.job_id
	WHERE workspace_builds.workspace_id = workspaces.id
	ORDER BY build_number DESC
	LIMIT 1
) latest_build ON true
LEFT JOIN LATERAL (
	SELECT
		workspace_agents.id as agent_id,
		workspace_agents.name as agent_name,
		job_id
	FROM workspace_resources
	JOIN workspace_agents ON (
		workspace_agents.resource_id = workspace_resources.id
		-- Filter out deleted sub agents.
		AND workspace_agents.deleted = FALSE
	)
	WHERE job_id = latest_build.job_id
) resources ON true
WHERE
	-- Filter by owner_id
	workspaces.owner_id = @owner_id :: uuid
	AND workspaces.deleted = false
	-- Authorize Filter clause will be injected below in GetAuthorizedWorkspacesAndAgentsByOwnerID
	-- @authorize_filter
GROUP BY workspaces.id, workspaces.name, latest_build.job_status, latest_build.job_id, latest_build.transition;

-- name: GetWorkspacesByTemplateID :many
SELECT * FROM workspaces WHERE template_id = $1 AND deleted = false;

-- name: UpdateWorkspaceACLByID :exec
UPDATE
	workspaces
SET
	group_acl = @group_acl,
	user_acl = @user_acl
WHERE
	id = @id;
