// Exercise a complete directory beyond both request budgets through a real
// browser, gateway, and SSH server, including a server-side secret exclusion.

import { cpSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { expect, test } from './fixtures'
import { OnboardingWizard } from './pages/wizard'

const claudeFixture = fileURLToPath(new URL('./testdata/claude-profile', import.meta.url))

test('a directory exceeding request budgets imports completely and reports policy exclusions', async ({
  page,
  aether,
}) => {
  const alice = await aether.member('alice')
  const repo = await aether.seedRepo('project')
  const source = join(alice.home, 'claude-profile')
  cpSync(claudeFixture, source, { recursive: true })
  const destinationSpecificFiles = {
    'cache/plugin.json': '{"cache":"configured"}',
    'logs/plugin.json': '{"log":"configured"}',
    'run/plugin.json': '{"run":"configured"}',
  }
  for (const [path, content] of Object.entries(destinationSpecificFiles)) {
    const filename = join(source, path)
    mkdirSync(dirname(filename), { recursive: true })
    writeFileSync(filename, content)
  }
  const dependencies = join(source, 'extensions', 'dependencies')
  mkdirSync(dependencies, { recursive: true })
  for (let index = 0; index < 2001; index += 1) {
    writeFileSync(join(dependencies, `${index}.js`), 'export {}\n')
  }
  const largeAsset = Buffer.alloc(64 * 1024 * 1024, 0x61)
  writeFileSync(join(source, 'extensions', 'large.bin'), largeAsset)

  const wizard = await OnboardingWizard.open(page, alice.url)
  await wizard.link.link(aether.server.addr, { name: 'Alice' })
  await wizard.link.continue().click()
  // The git identity is optional and this scenario is not about it.
  await wizard.gitIdentity.skip().click()
  await wizard.workspace.create('project')
  await wizard.repository.addRemote(repo)
  await wizard.repository.continue().click()
  await wizard.expectStep('Agents')

  const configuration = wizard.agents.configuration
  await configuration.chooseDirectory(source)
  await expect(configuration.preview()).toHaveCount(0)
  await expect(configuration.section).toContainText('claude-profile')

  // The fixture basename is intentionally not a harness root. The small
  // destination selector is the explicit fallback for such directories.
  await configuration.destination().selectOption('omp')
  await expect(configuration.preview()).toBeVisible()
  for (const path of Object.keys(destinationSpecificFiles)) {
    await expect(configuration.section.getByText(path, { exact: true })).toBeVisible()
  }
  await configuration.destination().selectOption('claude')
  await expect(configuration.preview()).toBeVisible()
  await configuration.section.getByText(/^Accepted paths:/).click()
  for (const path of Object.keys(destinationSpecificFiles)) {
    await expect(configuration.section.getByText(path, { exact: true })).toBeVisible()
  }
  await configuration.import().click()

  await expect(configuration.section).toContainText('Imported 2010 files')
  await expect(configuration.section).toContainText('README.md')
  await expect(configuration.section).toContainText('secret')
  await expect(configuration.import()).toHaveCount(0)
  for (const [path, content] of Object.entries(destinationSpecificFiles)) {
    const imported = await alice.api.rpc<{ content: string }>('config.read', {
      harness: 'claude',
      path,
    })
    expect(imported.content).toBe(content)
  }
  const settings = await alice.api.rpc<{ content: string; revision: string }>('config.read', {
    harness: 'claude',
    path: 'settings.json',
  })
  expect(settings.content).toContain('"theme": "dark"')
  const empty = await alice.api.rpc<{ content: string; size: number }>('config.read', {
    harness: 'claude',
    path: 'skills/deploy/notes.md',
  })
  expect(empty.content).toBe('')
  expect(empty.size).toBe(0)
  const dependency = await alice.api.rpc<{ content: string }>('config.read', {
    harness: 'claude',
    path: 'extensions/dependencies/2000.js',
  })
  expect(dependency.content).toBe('export {}\n')
  const { member } = await alice.api.rpc<{ member: { id: string } }>('server.info')
  const persistedAsset = readFileSync(
    join(aether.server.memberHome(member.id), '.claude', 'extensions', 'large.bin'),
  )
  expect(persistedAsset.equals(largeAsset)).toBe(true)
  const edited = await alice.api.rpc<{ content: string }>('config.write', {
    harness: 'claude',
    path: 'settings.json',
    content: '{"theme":"light"}',
    revision: settings.revision,
  })
  expect(edited.content).toBe('{"theme":"light"}')
})
