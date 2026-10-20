import { describe, expect, it, vi } from 'vitest';
import { ApiError } from './api';
import { loadProject } from './projectLoad';
import type { Project, Role } from './types';

const project = (role: Role | undefined): Project =>
  ({ id: 7, slug: 'p', name: 'P', created_at: '2026-01-01T00:00:00Z', role }) as Project;

describe('loadProject', () => {
  const cases: Array<[Role, boolean, boolean]> = [
    ['viewer', false, false],
    ['member', false, true],
    ['owner', true, true]
  ];

  it.each(cases)('derives the access of a %s from the project alone', async (role, owner, canWrite) => {
    const api = { getProject: vi.fn().mockResolvedValue(project(role)) };
    const data = await loadProject(api, 7);
    expect(data.access).toEqual({ role, owner, canWrite });
    expect(data.projectId).toBe(7);
    expect(data.project.role).toBe(role);
    expect(api.getProject).toHaveBeenCalledTimes(1);
    expect(api.getProject).toHaveBeenCalledWith(7);
  });

  it('turns a 404 into a not found page', async () => {
    const api = { getProject: vi.fn().mockRejectedValue(new ApiError(404, 'gone')) };
    await expect(loadProject(api, 7)).rejects.toMatchObject({ status: 404 });
  });

  it('refuses an answer without a known role instead of assuming ownership', async () => {
    const api = { getProject: vi.fn().mockResolvedValue(project(undefined)) };
    await expect(loadProject(api, 7)).rejects.toMatchObject({ status: 404 });
  });
});
