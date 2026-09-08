import { Trans, useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
import { useCopy } from '../hooks/useCopy'

// Docs page. Ported 1:1 from internal/web/app.html:358-405, with the dynamic
// bits (curl example, install prompt, /install.md + /skill.md links) built from
// location.origin per app.js:451-468.

// Comment lines inside the code blocks are localized too: they are prose the
// reader follows, not commands they run.
function curlExample(origin: string, t: TFunction): string {
  return [
    "curl -X POST '" + origin + "/api/publish' \\",
    "  -H 'Authorization: Bearer pl_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx' \\",
    "  -F 'file=@page.html' \\",
    "  -F 'title=" + t('docs.curl.title') + "' \\",
    "  -F 'expiry=30d'",
  ].join('\n')
}

function cliInstall(origin: string, t: TFunction): string {
  return [
    '# macOS / Linux',
    'curl -fsSL ' + origin + '/install.sh | sh',
    '',
    '# Windows (PowerShell)',
    'irm ' + origin + '/install.ps1 | iex',
    '',
    t('docs.cliInstall.loginComment'),
    'placard login --base ' + origin,
  ].join('\n')
}

function cliUsage(t: TFunction): string {
  return [
    t('docs.cliUsage.publish'),
    'placard publish page.html',
    '',
    t('docs.cliUsage.list'),
    'placard ls',
    '',
    t('docs.cliUsage.open'),
    'placard open <id>',
    '',
    t('docs.cliUsage.visibility'),
    'placard share set <id> --visibility link',
    '',
    t('docs.cliUsage.password'),
    'placard publish page.html --password auto',
    '',
    t('docs.cliUsage.remove'),
    'placard rm <id>',
    '',
    t('docs.cliUsage.update'),
    'placard update',
  ].join('\n')
}

// A block of shell text plus its copy button. Each line is its own block <span>
// so a comment line can be greyed out independently.
function CodeBlock({
  text,
  copyLabel,
  onCopy,
  preformatted,
}: {
  text: string
  copyLabel: string
  onCopy: () => void
  // The install prompt is prose that wraps; the shell blocks scroll instead.
  preformatted?: boolean
}) {
  const { t } = useTranslation()
  return (
    <div
      className="flex items-start gap-2 p-2 pl-4 rounded-xl border border-line-strong mb-3"
      style={{ background: 'var(--bg)' }}
    >
      <pre
        className="flex-1 mono text-xs leading-relaxed"
        style={
          preformatted
            ? { whiteSpace: 'pre-wrap', wordBreak: 'break-all', margin: '.25rem 0' }
            : { margin: '.25rem 0', overflowX: 'auto' }
        }
      >
        {preformatted
          ? text
          : text.split('\n').map((line, i) => (
              <span
                key={i}
                className={line.startsWith('#') ? 'text-soft' : undefined}
                style={{ display: 'block' }}
              >
                {line || ' '}
              </span>
            ))}
      </pre>
      <button
        aria-label={copyLabel}
        className="btn-ink shrink-0 rounded-lg text-sm font-medium px-4 py-2"
        onClick={onCopy}
      >
        {t('common.copy')}
      </button>
    </div>
  )
}

export default function DocsPage() {
  const copy = useCopy()
  const { t } = useTranslation()
  const origin = location.origin

  const CURL_EXAMPLE = curlExample(origin, t)
  const INSTALL_PROMPT = t('docs.installPrompt', { origin })
  const CLI_INSTALL = cliInstall(origin, t)
  const CLI_USAGE = cliUsage(t)

  return (
    <section className="pb-16">
      <div className="pt-12 pb-8">
        <h1 className="font-display font-extrabold text-4xl sm:text-5xl tracking-[-0.02em] mb-2">
          {t('docs.title')}
        </h1>
        <p className="text-soft max-w-2xl leading-relaxed">{t('docs.lead')}</p>
      </div>

      <div className="rounded-2xl border border-line surface p-6 mb-6">
        <h2 className="font-display font-bold text-lg mb-3">{t('docs.skill.title')}</h2>
        <p className="text-sm leading-relaxed mb-3">{t('docs.skill.desc')}</p>
        <CodeBlock
          text={INSTALL_PROMPT}
          preformatted
          copyLabel={t('docs.skill.copyAria')}
          onCopy={() => copy(INSTALL_PROMPT, t('docs.skill.copied'))}
        />
        <p className="text-soft text-sm leading-relaxed">
          <Trans
            i18nKey="docs.skill.links"
            components={{
              install: (
                <a
                  className="mono text-[0.85em] ul"
                  style={{ color: 'var(--fg)' }}
                  href="/install.md"
                  target="_blank"
                  rel="noopener"
                >
                  {origin + '/install.md'}
                </a>
              ),
              skill: (
                <a
                  className="mono text-[0.85em] ul"
                  style={{ color: 'var(--fg)' }}
                  href="/skill.md"
                  target="_blank"
                  rel="noopener"
                >
                  {origin + '/skill.md'}
                </a>
              ),
            }}
          />
        </p>
      </div>

      <div className="rounded-2xl border border-line surface p-6 mb-6">
        <h2 className="font-display font-bold text-lg mb-3">{t('docs.publishMethods.title')}</h2>
        <p className="text-sm leading-relaxed mb-2">
          <Trans
            i18nKey="docs.publishMethods.web"
            components={{ b: <span className="font-semibold" />, code: <span className="mono text-[0.85em]" /> }}
          />
        </p>
        <p className="text-sm leading-relaxed mb-2">
          <Trans
            i18nKey="docs.publishMethods.cli"
            components={{ b: <span className="font-semibold" />, code: <span className="mono text-[0.85em]" /> }}
          />
        </p>
        <p className="text-soft text-xs mb-1">{t('docs.publishMethods.installLabel')}</p>
        <CodeBlock
          text={CLI_INSTALL}
          copyLabel={t('docs.publishMethods.installCopyAria')}
          onCopy={() => copy(CLI_INSTALL, t('docs.publishMethods.installCopied'))}
        />
        <p className="text-soft text-xs mb-1">{t('docs.publishMethods.usageLabel')}</p>
        <CodeBlock
          text={CLI_USAGE}
          copyLabel={t('docs.publishMethods.usageCopyAria')}
          onCopy={() => copy(CLI_USAGE, t('docs.publishMethods.usageCopied'))}
        />
        <p className="text-sm leading-relaxed mb-3">
          <Trans
            i18nKey="docs.publishMethods.api"
            components={{ b: <span className="font-semibold" />, code: <span className="mono text-[0.85em]" /> }}
          />
        </p>
        <CodeBlock
          text={CURL_EXAMPLE}
          preformatted
          copyLabel={t('docs.publishMethods.curlCopyAria')}
          onCopy={() => copy(CURL_EXAMPLE, t('docs.publishMethods.curlCopied'))}
        />
        <p className="text-soft text-sm leading-relaxed">
          <Trans
            i18nKey="docs.publishMethods.expiry"
            components={{
              code: <span className="mono text-[0.85em]" />,
              // Angle brackets never reach the translation string: Trans parses
              // it as markup, so `<n>d` there would be read as a tag.
              duration: <span className="mono text-[0.85em]">{'<n>d / <n>w / <n>y'}</span>,
              update: <span className="mono text-[0.85em]">{'-F "id=<id>"'}</span>,
            }}
          />
        </p>
      </div>

      <div className="rounded-2xl border border-line surface p-6 mb-6">
        <h2 className="font-display font-bold text-lg mb-3">{t('docs.visibility.title')}</h2>
        <p className="text-sm leading-relaxed mb-2">
          <Trans
            i18nKey="docs.visibility.link"
            components={{
              url: <span className="mono text-[0.85em]">{origin + '/s/<id>'}</span>,
            }}
          />
        </p>
        <p className="text-sm leading-relaxed mb-2">
          <Trans i18nKey="docs.visibility.tiers" components={{ b: <span className="font-semibold" /> }} />
        </p>
        <p className="text-sm leading-relaxed">
          <Trans i18nKey="docs.visibility.api" components={{ code: <span className="mono text-[0.85em]" /> }} />
        </p>
      </div>

      <div className="rounded-2xl border border-line surface p-6 mb-6">
        <h2 className="font-display font-bold text-lg mb-3">{t('docs.shareCode.title')}</h2>
        <p className="text-sm leading-relaxed mb-2">{t('docs.shareCode.body')}</p>
        <p className="text-sm leading-relaxed">
          <Trans i18nKey="docs.shareCode.rotate" components={{ code: <span className="mono text-[0.85em]" /> }} />
        </p>
      </div>

      <div className="rounded-2xl border border-line surface p-6 mb-6">
        <h2 className="font-display font-bold text-lg mb-3">{t('docs.lifecycle.title')}</h2>
        <p className="text-sm leading-relaxed">
          <Trans i18nKey="docs.lifecycle.body" components={{ b: <span className="font-semibold" /> }} />
        </p>
      </div>

      <div className="rounded-2xl border border-line surface p-6">
        <h2 className="font-display font-bold text-lg mb-3">{t('docs.limits.title')}</h2>
        <div className="text-sm">
          <div className="flex justify-between py-2 border-b border-line">
            <span className="text-soft">{t('docs.limits.fileSize')}</span>
            <span>{t('docs.limits.fileSizeValue')}</span>
          </div>
          <div className="flex justify-between py-2 border-b border-line">
            <span className="text-soft">{t('docs.limits.fileType')}</span>
            <span>{t('docs.limits.fileTypeValue')}</span>
          </div>
          <div className="flex justify-between py-2 border-b border-line">
            <span className="text-soft">{t('docs.limits.rate')}</span>
            <span>{t('docs.limits.rateValue')}</span>
          </div>
          <div className="flex justify-between py-2">
            <span className="text-soft">{t('docs.limits.tokenTTL')}</span>
            <span>{t('docs.limits.tokenTTLValue')}</span>
          </div>
        </div>
      </div>
    </section>
  )
}
