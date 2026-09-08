import { fireEvent, render, screen, within } from '@testing-library/react'
import { beforeEach, describe, expect, test, vi } from 'vitest'
import { ToastProvider } from '../hooks/useToast'
import DocsPage from './DocsPage'

function renderDocs() {
  return render(
    <ToastProvider>
      <DocsPage />
    </ToastProvider>,
  )
}

const writeText = vi.fn((_text: string) => Promise.resolve())

// There are several <pre> blocks on the page now (CLI, curl), so pick by content
// rather than by document order — order is presentation, not contract.
function preContaining(needle: string): HTMLElement | undefined {
  return Array.from(document.querySelectorAll('pre')).find((p) =>
    p.textContent?.includes(needle),
  ) as HTMLElement | undefined
}

beforeEach(() => {
  writeText.mockClear()
  vi.stubGlobal('navigator', { ...navigator, clipboard: { writeText } })
})

describe('DocsPage', () => {
  test('curl block contains origin/api/publish and Bearer pl_', () => {
    renderDocs()
    const curl = preContaining('/api/publish')
    expect(curl?.textContent).toContain(`${location.origin}/api/publish`)
    expect(curl?.textContent).toContain('Bearer pl_')
  })

  test('install block serves both scripts from origin and shows login', () => {
    renderDocs()
    const install = preContaining('placard login')
    expect(install?.textContent).toContain(`${location.origin}/install.sh`)
    // Windows line must go through origin too, never a hardcoded release host.
    expect(install?.textContent).toContain(`${location.origin}/install.ps1`)
    expect(install?.textContent).not.toContain('dl.placard.example.com')
  })

  test('usage block lists real commands and no bare `placard share <id>`', () => {
    renderDocs()
    const usage = preContaining('placard publish')
    expect(usage?.textContent).toContain('placard rm')
    // `share` is a command group, not a link-copy command. Match whole lines:
    // a substring check would be satisfied by the legitimate `share set` line.
    // Each line is rendered as its own block <span>, so read those rather than
    // splitting textContent (which concatenates spans without newlines).
    const lines = Array.from(usage?.querySelectorAll('span') ?? []).map((s) => s.textContent?.trim())
    expect(lines).not.toContain('placard share <id>')
    expect(lines).toContain('placard share set <id> --visibility link')
  })

  test('clicking Copy on the install block calls clipboard writeText', () => {
    renderDocs()
    fireEvent.click(screen.getByRole('button', { name: 'Copy the install commands' }))
    expect(writeText).toHaveBeenCalledTimes(1)
    expect(writeText.mock.calls[0][0]).toContain('placard login')
  })

  test('clicking Copy on the usage block calls clipboard writeText', () => {
    renderDocs()
    fireEvent.click(screen.getByRole('button', { name: 'Copy the usage examples' }))
    expect(writeText).toHaveBeenCalledTimes(1)
    expect(writeText.mock.calls[0][0]).toContain('placard publish')
  })

  test('install prompt contains origin/install.md', () => {
    renderDocs()
    expect(screen.getByText(new RegExp(`Read ${location.origin}/install.md`))).toBeInTheDocument()
  })

  test('install and skill link hrefs are /install.md and /skill.md', () => {
    renderDocs()
    const install = screen.getByRole('link', { name: `${location.origin}/install.md` })
    const skill = screen.getByRole('link', { name: `${location.origin}/skill.md` })
    expect(install).toHaveAttribute('href', '/install.md')
    expect(skill).toHaveAttribute('href', '/skill.md')
  })

  test('the limits table shows the size cap and the publish rate', () => {
    renderDocs()
    expect(screen.getByText('10MB')).toBeInTheDocument()
    expect(screen.getByText('50 per user per hour')).toBeInTheDocument()
  })

  test('clicking Copy on curl calls clipboard writeText', () => {
    renderDocs()
    fireEvent.click(screen.getByRole('button', { name: 'Copy the curl example' }))
    expect(writeText).toHaveBeenCalledTimes(1)
    expect(writeText.mock.calls[0][0]).toContain(`${location.origin}/api/publish`)
  })

  test('the page-management section mentions versioned republish with a stable link', () => {
    renderDocs()
    // Scope to the page-management card so the assertion can't be satisfied by
    // an unrelated "version" mention elsewhere (e.g. the API section).
    const heading = screen.getByText('Managing pages')
    const section = within(heading.closest('div') as HTMLElement)
    expect(section.getByText('the link stays the same')).toBeInTheDocument()
    expect(section.getByText(/version/)).toBeInTheDocument()
  })

  test('the visibility section documents the /s/ link shape and that Link needs no account', () => {
    renderDocs()
    const heading = screen.getByText('Visibility')
    const section = within(heading.closest('div') as HTMLElement)
    expect(section.getByText(`${location.origin}/s/<id>`)).toBeInTheDocument()
    expect(section.getByText(/anyone holding the link can read it, no sign-in/)).toBeInTheDocument()
  })
})
