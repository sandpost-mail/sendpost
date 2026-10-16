import { api, errorMessage } from './api';
import type { Project } from './types';

const LAST_KEY = 'sandpost.lastProject';

/** Shared list of projects for the switcher. */
class ProjectsStore {
  items = $state<Project[]>([]);
  loaded = $state(false);
  error = $state('');

  async load(): Promise<void> {
    try {
      this.items = await api.listProjects();
      this.error = '';
    } catch (e) {
      this.error = errorMessage(e);
    } finally {
      this.loaded = true;
    }
  }

  add(project: Project): void {
    this.items = [...this.items.filter((p) => p.id !== project.id), project].sort((a, b) =>
      a.name.localeCompare(b.name)
    );
  }
}

export const projects = new ProjectsStore();

/** The project last opened, remembered per browser. Storage may be unavailable. */
export function lastProjectId(): number | null {
  try {
    const raw = localStorage.getItem(LAST_KEY);
    const id = raw ? Number(raw) : NaN;
    return Number.isInteger(id) && id > 0 ? id : null;
  } catch {
    return null;
  }
}

export function rememberProject(id: number): void {
  try {
    localStorage.setItem(LAST_KEY, String(id));
  } catch {
    // private mode or blocked storage: the switcher still works
  }
}
