-- Highest role rank (3 owner, 2 member, 1 viewer, 0 none) the user holds on a
-- project through its groups. No row means the project does not exist.
-- name: ProjectRoleRank :one
SELECT COALESCE(max(CASE pg.role WHEN 'owner' THEN 3 WHEN 'member' THEN 2 WHEN 'viewer' THEN 1 ELSE 0 END), 0)::int AS rank
FROM projects p
LEFT JOIN project_groups pg ON pg.project_id = p.id
     AND pg.group_id IN (SELECT gm.group_id FROM group_members gm WHERE gm.user_id = @user_id)
WHERE p.id = @project_id
GROUP BY p.id;

-- name: ProjectExists :one
SELECT EXISTS (SELECT 1 FROM projects WHERE id = $1);
