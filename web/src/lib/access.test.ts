import { describe, expect, it, vi } from 'vitest';
import { projectAccess } from './access';
import { ApiError } from './api';

describe('projectAccess', () => {
  it('makes administrators owners without asking the API', async () => {
    const api = { listProjectGroups: vi.fn() };
    expect(await projectAccess(api, { is_admin: true }, 1)).toEqual({ owner: true });
    expect(api.listProjectGroups).not.toHaveBeenCalled();
  });

  it('detects owners by the owner only endpoint', async () => {
    const api = { listProjectGroups: vi.fn().mockResolvedValue([]) };
    expect(await projectAccess(api, { is_admin: false }, 3)).toEqual({ owner: true });
    expect(api.listProjectGroups).toHaveBeenCalledWith(3);
  });

  it('treats a 403 as not owner', async () => {
    const api = { listProjectGroups: vi.fn().mockRejectedValue(new ApiError(403, 'forbidden')) };
    expect(await projectAccess(api, { is_admin: false }, 3)).toEqual({ owner: false });
  });

  it('rethrows other failures, including a pending password change', async () => {
    const fail = (e: Error) => ({ listProjectGroups: vi.fn().mockRejectedValue(e) });
    await expect(projectAccess(fail(new ApiError(404, 'gone')), null, 1)).rejects.toThrow('gone');
    await expect(
      projectAccess(fail(new ApiError(403, 'change', 'password_change_required')), null, 1)
    ).rejects.toThrow('change');
  });
});
