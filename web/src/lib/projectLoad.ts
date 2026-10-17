import { error } from '@sveltejs/kit';
import { accessFor, isRole, type ProjectAccess } from './access';
import { failLoad } from './loadError';
import type { Project } from './types';

export interface ProjectLayoutData {
  projectId: number;
  project: Project;
  access: ProjectAccess;
}

/**
 * Loads a project and what the caller may do in it with one request: the
 * effective role comes with the project, so nothing is probed.
 */
export async function loadProject(
  api: { getProject: (id: number) => Promise<Project> },
  projectId: number
): Promise<ProjectLayoutData> {
  let project: Project;
  try {
    project = await api.getProject(projectId);
  } catch (e) {
    failLoad(e, 'project');
  }
  // An answer without a known role is treated like no access, never as owner.
  if (!isRole(project.role)) error(404, 'Project not found');
  return { projectId, project, access: accessFor(project.role) };
}
