// The onboarding wizard, as the page object every scenario drives.
//
// Adding a step to the wizard means adding one class here and one field on
// OnboardingWizard: give the class the `aria-label` of the step's <section>
// and the step's own actions, and add its label to `stepNames` in the order
// the header lists it. Nothing else in the suite changes.

import { expect, type Locator, type Page } from '@playwright/test'

/** The step labels the wizard's header lists, in order. */
export const stepNames = [
  'Link',
  'Git identity',
  'Workspace',
  'Repository',
  'Agents',
  'First run',
] as const

export type StepName = (typeof stepNames)[number]

/**
 * One step. `section` is the step's own <section aria-label>, so a locator
 * built from it can never reach another step's controls.
 */
class Step {
  constructor(
    protected readonly page: Page,
    readonly label: StepName,
  ) {}

  get section(): Locator {
    return this.page.getByRole('region', { name: this.label, exact: true })
  }

  button(name: string | RegExp): Locator {
    return this.section.getByRole('button', { name, exact: true })
  }
}

export class LinkStep extends Step {
  constructor(page: Page) {
    super(page, 'Link')
  }

  async link(addr: string, options: { invite?: string; name?: string } = {}): Promise<void> {
    await this.section.getByLabel('Server address').fill(addr)
    if (options.invite) await this.section.getByLabel('Invite code').fill(options.invite)
    if (options.name) await this.section.getByLabel('Your name').fill(options.name)
    await this.button('Link').click()
  }

  continue(): Locator {
    return this.button('Continue')
  }
}

export class GitIdentityStep extends Step {
  constructor(page: Page) {
    super(page, 'Git identity')
  }

  /** Fills the identity in and saves it, which also moves the wizard on. */
  async save(name: string, email: string): Promise<void> {
    await this.section.getByLabel('Name', { exact: true }).fill(name)
    await this.section.getByLabel('Email', { exact: true }).fill(email)
    await this.button('Save').click()
  }

  /** Moves on without one, leaving the server's fallback in place. */
  skip(): Locator {
    return this.button('Skip')
  }
}

export class WorkspaceStep extends Step {
  constructor(page: Page) {
    super(page, 'Workspace')
  }

  async create(name: string, baseBranch = 'main'): Promise<void> {
    await this.section.getByLabel('Name', { exact: true }).fill(name)
    await this.section.getByLabel('Base branch').fill(baseBranch)
    await this.button('Create workspace').click()
  }

  /** Picks a workspace the server already has. */
  use(name: string): Locator {
    return this.section.getByRole('button', { name: `Use ${name}`, exact: true })
  }
}

export class RepositoryStep extends Step {
  constructor(page: Page) {
    super(page, 'Repository')
  }

  async addRemote(repoPath: string): Promise<void> {
    await this.section.getByLabel('Repository path').fill(repoPath)
    await this.button('Add remote').click()
  }

  push(): Locator {
    return this.button('Push now')
  }

  /** The "What git did" panel: git's own output, verbatim. */
  gitOutput(): Locator {
    return this.section.getByRole('group').filter({ hasText: 'What git did' }).locator('pre')
  }

  continue(): Locator {
    return this.button('Continue')
  }
}

export class AgentsStep extends Step {
  constructor(page: Page) {
    super(page, 'Agents')
  }

  /** Opens a harness's setup screen. `label` is the name the list shows. */
  setUp(label: string): Locator {
    return this.section.getByRole('button', { name: `Set up ${label}`, exact: true })
  }

  /** The setup screen's confirmation, which also saves the environment. */
  confirmInstalled(): Locator {
    return this.button("I've installed and logged in")
  }

  /** The terminal dock's overlay while the environment container starts. */
  containerStarting(): Locator {
    return this.section.getByRole('status')
  }

  /** Opens the Connect GitHub sub-screen. */
  connectGitHub(): Locator {
    return this.button('Connect GitHub')
  }

  skip(): Locator {
    return this.button('Skip for now')
  }

  get configuration(): ConfigurationImport {
    return new ConfigurationImport(this.page)
  }

  get github(): GitHubConnect {
    return new GitHubConnect(this.page)
  }
}

/**
 * The Agents step's GitHub part, closed and open: both states carry the
 * same `<section aria-label>` and never render together, so one object
 * covers them.
 */
export class GitHubConnect {
  constructor(private readonly page: Page) {}

  get section(): Locator {
    return this.page.getByRole('region', { name: 'Connect GitHub', exact: true })
  }

  /** Runs the non-interactive half, once the device login is done. */
  confirmLoggedIn(): Locator {
    return this.section.getByRole('button', { name: "I've logged in", exact: true })
  }
}

/**
 * The Agents step's second half: bringing this machine's agent
 * configuration to the server. It has its own <section aria-label>, so it
 * gets its own object rather than crowding the step.
 */
export class ConfigurationImport {
  constructor(private readonly page: Page) {}

  get section(): Locator {
    return this.page.getByRole('region', {
      name: 'Bring your configuration',
      exact: true,
    })
  }

  look(): Locator {
    return this.section.getByRole('button', {
      name: 'Look at what is here',
      exact: true,
    })
  }

  /** One harness's row, by the name the list shows. */
  row(label: string): Locator {
    return this.section.getByRole('listitem').filter({ hasText: label })
  }

  select(label: string): Locator {
    return this.section.getByRole('checkbox', {
      name: `Bring ${label} configuration`,
      exact: true,
    })
  }

  import(): Locator {
    return this.section.getByRole('button', { name: 'Import selected', exact: true })
  }
}

export class FirstRunStep extends Step {
  constructor(page: Page) {
    super(page, 'First run')
  }

  /**
   * Launches the run. A harness the server does not list - `fake` is a
   * scheduler registration, not a registry entry - goes in through the
   * select's own "Other..." option.
   */
  async launch(harness: string, task: string): Promise<void> {
    const select = this.section.getByRole('combobox', { name: 'Harness' })
    const listed = await select.locator(`option[value="${harness}"]`).count()
    if (listed > 0) {
      await select.selectOption(harness)
    } else {
      await select.selectOption('__custom')
      await this.section
        .getByRole('textbox', { name: 'Harness name' })
        .fill(harness)
    }
    await this.section.getByRole('textbox', { name: 'Task' }).fill(task)
    await this.button('Launch').click()
  }
}

export class OnboardingWizard {
  readonly link: LinkStep
  readonly gitIdentity: GitIdentityStep
  readonly workspace: WorkspaceStep
  readonly repository: RepositoryStep
  readonly agents: AgentsStep
  readonly firstRun: FirstRunStep

  constructor(readonly page: Page) {
    this.link = new LinkStep(page)
    this.gitIdentity = new GitIdentityStep(page)
    this.workspace = new WorkspaceStep(page)
    this.repository = new RepositoryStep(page)
    this.agents = new AgentsStep(page)
    this.firstRun = new FirstRunStep(page)
  }

  /**
   * Opens the dashboard on the member's tokened URL. An unlinked gateway
   * routes itself to the wizard; a linked one is already past it, so the
   * sidebar entry is the way back in.
   */
  static async open(page: Page, url: string): Promise<OnboardingWizard> {
    const wizard = new OnboardingWizard(page)
    await page.goto(url)
    const heading = page.getByRole('heading', { name: 'Onboarding', exact: true })
    if (!(await heading.isVisible())) {
      await page.getByRole('button', { name: 'Onboarding', exact: true }).click()
    }
    await expect(heading).toBeVisible()
    return wizard
  }

  /** The step the header marks as current. */
  currentStep(): Locator {
    return this.page.getByRole('list', { name: 'Steps' }).locator('[aria-current="step"]')
  }

  async expectStep(name: StepName): Promise<void> {
    const index = stepNames.indexOf(name)
    await expect(this.currentStep()).toHaveText(`${index + 1}. ${name}`)
  }

  /** Closes an open sub-screen, and only then leaves the step. */
  back(): Locator {
    return this.page.getByRole('button', { name: 'Back', exact: true })
  }
}
