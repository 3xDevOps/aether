// The onboarding wizard, as the page object every scenario drives.
//
// Adding a step to the wizard means adding one class here and one field on
// OnboardingWizard: give the class the `aria-label` of the step's <section>
// and the step's own actions, and add its label to `stepNames` in the order
// the header lists it. Nothing else in the suite changes.

import { expect, type Locator, type Page } from '@playwright/test'

/** The step labels the wizard's header lists, in order. */
export const stepNames = ['Connect', 'Repository', 'Agent', 'First run'] as const

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

  /** Opens a collapsed disclosure of the step, named by its caption. */
  async expand(caption: string): Promise<void> {
    const trigger = this.section.getByRole('button', { name: new RegExp(`^${caption}`) })
    if ((await trigger.getAttribute('aria-expanded')) !== 'true') await trigger.click()
  }
}

export class ConnectStep extends Step {
  constructor(page: Page) {
    super(page, 'Connect')
  }

  /** Opens the address form, which sits behind a disclosure below the sign-in. */
  byAddress(): Locator {
    return this.section.getByRole('button', { name: /^Link by address/ })
  }

  async link(addr: string, options: { invite?: string; name?: string } = {}): Promise<void> {
    await this.expand('Link by address')
    await this.section.getByLabel('Server address').fill(addr)
    if (options.invite) await this.section.getByLabel('Invite code').fill(options.invite)
    if (options.name) await this.section.getByLabel('Your name').fill(options.name)
    await this.button('Link').click()
  }

  continue(): Locator {
    return this.button('Continue')
  }

  /** The git identity form at the bottom of the step, once the server is linked. */
  get identity(): GitIdentity {
    return new GitIdentity(this.section)
  }
}

export class GitIdentity {
  constructor(private readonly scope: Locator) {}

  get form(): Locator {
    return this.scope.getByRole('form', { name: 'Git identity' })
  }

  async save(name: string, email: string): Promise<void> {
    await this.form.getByLabel('Name', { exact: true }).fill(name)
    await this.form.getByLabel('Email', { exact: true }).fill(email)
    await this.form.getByRole('button', { name: 'Save identity' }).click()
    await expect(this.form.getByRole('status')).toHaveText('Saved')
  }
}

export class RepositoryStep extends Step {
  constructor(page: Page) {
    super(page, 'Repository')
  }

  async createFromClone(name: string, baseBranch = 'main'): Promise<void> {
    await this.button('Create from local clone').click()
    await this.section.getByLabel('Workspace name', { exact: true }).fill(name)
    await this.section.getByLabel('Base branch').fill(baseBranch)
    await this.button('Create workspace').click()
  }

  /** Picks a workspace the server already has. */
  use(name: string): Locator {
    return this.section.getByRole('button', { name: `Use ${name}`, exact: true })
  }

  /** Back to the workspace list from a chosen workspace. */
  change(): Locator {
    return this.button('Choose another workspace')
  }

  localClone(): Locator {
    return this.button('Link local repository')
  }

  async addRemote(repoPath: string): Promise<void> {
    await this.section.getByLabel('Repository path').fill(repoPath)
    await this.button('Add remote').click()
  }

  push(): Locator {
    return this.button('Push now')
  }

  /** The "What git did" panel: git's own output, verbatim. A `Collapsible`
   * carries no role of its own, so this goes by the slot instead. */
  gitOutput(): Locator {
    return this.section
      .locator('[data-slot="collapsible"]')
      .filter({ hasText: 'What git did' })
      .locator('pre')
  }

  continue(): Locator {
    return this.button('Continue')
  }

  /** What a member who cannot add a workspace gets instead of the two cards. */
  continueToAgent(): Locator {
    return this.button('Continue to Agent')
  }

  get identity(): GitIdentity {
    return new GitIdentity(this.section)
  }
}

export class AgentStep extends Step {
  constructor(page: Page) {
    super(page, 'Agent')
  }

  /** Opens an agent's setup. `label` is the name the list shows. */
  setUp(label: string): Locator {
    return this.section.getByRole('button', { name: `Set up ${label}`, exact: true })
  }

  /** The list's row for an agent. */
  row(label: string): Locator {
    return this.section.getByRole('list', { name: 'Agents' }).getByRole('listitem').filter({ hasText: label })
  }

  /** A mode card of the Standard and Enhanced comparison. */
  mode(name: 'Standard' | 'Enhanced'): Locator {
    return this.section.getByRole('radio', { name, exact: true })
  }

  install(label: string): Locator {
    return this.button(`Install ${label}`)
  }

  /** What step 3 of the setup read back from agent.list. */
  status(label: string): Locator {
    return this.section.getByRole('list', { name: `${label} status` })
  }

  check(): Locator {
    return this.section.getByRole('button', { name: /^Check( again)?$/ })
  }

  done(): Locator {
    return this.button('Done')
  }

  /** The terminal dock's overlay while the environment container starts. */
  containerStarting(): Locator {
    return this.section.getByRole('status').filter({ hasText: 'Starting your environment container' })
  }

  /** Opens the Connect GitHub sub-screen from its disclosure. */
  async connectGitHub(): Promise<void> {
    await this.expand('GitHub')
    await this.button('Connect GitHub').click()
  }

  continue(): Locator {
    return this.button('Continue')
  }

  skip(): Locator {
    return this.button('Skip for now')
  }

  /** The importer, behind the Agent config files disclosure. */
  async configuration(): Promise<ConfigurationImport> {
    await this.expand('Agent config files')
    return new ConfigurationImport(this.page)
  }

  get github(): GitHubConnect {
    return new GitHubConnect(this.page)
  }
}

/**
 * The Agent step's GitHub part, closed and open: both states carry the
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

  /**
   * The screen's own copy of a command, in its code block. The section
   * also holds the terminal, which echoes whatever was typed into it, so
   * asserting on the section cannot tell the two apart.
   */
  get commands(): Locator {
    return this.section.locator('code')
  }
}

/**
 * One explicit browser directory import. The input is scoped to its section
 * so another file picker in the page cannot be mistaken for the
 * configuration source.
 */
export class ConfigurationImport {
  constructor(private readonly page: Page) {}

  get section(): Locator {
    return this.page.getByRole('region', {
      name: 'Agent config files',
      exact: true,
    })
  }

  directoryInput(): Locator {
    return this.section.getByLabel('Choose configuration directory')
  }

  async chooseDirectory(path: string): Promise<void> {
    await this.directoryInput().setInputFiles(path)
  }

  preview(): Locator {
    return this.section.getByText('Preview', { exact: true })
  }

  destination(): Locator {
    return this.section.getByLabel('Configuration destination')
  }

  import(): Locator {
    return this.section.getByRole('button', {
      name: 'Import configuration',
      exact: true,
    })
  }
}

export class FirstRunStep extends Step {
  constructor(page: Page) {
    super(page, 'First run')
  }

  /**
   * Launches the run from the launch dialog's own form. Only agents this
   * account has installed can be picked, so a scenario installs one first.
   */
  async launch(agent: string, task: string): Promise<void> {
    await this.section.getByRole('radio', { name: new RegExp(`^${agent}`, 'i') }).click()
    await this.section.getByRole('textbox', { name: 'Task' }).fill(task)
    await this.button('Launch').click()
  }

  /** The way out when no agent is installed: back to the Agent step. */
  setUpAgent(): Locator {
    return this.button('Set up an agent')
  }
}

export class OnboardingWizard {
  readonly connect: ConnectStep
  readonly repository: RepositoryStep
  readonly agent: AgentStep
  readonly firstRun: FirstRunStep

  constructor(readonly page: Page) {
    this.connect = new ConnectStep(page)
    this.repository = new RepositoryStep(page)
    this.agent = new AgentStep(page)
    this.firstRun = new FirstRunStep(page)
  }

  /**
   * Opens the dashboard on the member's tokened URL. An unlinked gateway
   * routes itself to the wizard; a linked one is already past it, so the
   * command palette is the way back in.
   */
  static async open(page: Page, url: string): Promise<OnboardingWizard> {
    const wizard = new OnboardingWizard(page)
    await page.goto(url)
    const heading = page.getByRole('heading', { name: 'Onboarding', exact: true })
    const live = page.getByRole('button', { name: /, Live$/ })
    await expect(heading.or(live).first()).toBeAttached()
    if (!(await heading.count())) {
      await page.getByRole('button', { name: 'Search', exact: true }).first().click()
      await page.getByRole('combobox').fill('Onboarding')
      await page.getByRole('option', { name: 'Onboarding', exact: true }).click()
    }
    await expect(heading).toBeAttached()
    return wizard
  }

  /** The step the header marks as current. */
  currentStep(): Locator {
    return this.page.getByRole('list', { name: 'Steps' }).locator('[aria-current="step"]')
  }

  async expectStep(name: StepName): Promise<void> {
    const current = this.currentStep()
    await expect(current).toHaveCount(1)
    await expect(current.getByText(name, { exact: true })).toBeVisible()
    await expect(this.page.getByRole('region', { name, exact: true })).toBeVisible()
  }

  /** Closes an open sub-screen, and only then leaves the step. */
  back(): Locator {
    return this.page.getByRole('button', { name: 'Back', exact: true })
  }
}
