// Canonical private/link vocabulary — single source of truth for the value set
// shared by FileRow's list badge, ShareDialog's picker, PublishPage's
// publish-time picker, and SettingsPage's default-visibility preference. The
// labels themselves live in the translation bundles under `visibility.<val>`.

import type { TFunction } from 'i18next'

export const VISIBILITY_VALUES = ['private', 'link'] as const

export interface VisibilityOption {
  val: string
  label: string
  // Spelled out wherever the user picks a tier: the one-word label does not say
  // that "link" means world-readable.
  hint: string
}

export function visibilityLabel(val: string, t: TFunction): string {
  return t('visibility.' + val + '.label', { defaultValue: val })
}

export function visibilityHint(val: string, t: TFunction): string {
  return t('visibility.' + val + '.hint', { defaultValue: '' })
}

export function visibilityOptions(t: TFunction): VisibilityOption[] {
  return VISIBILITY_VALUES.map((val) => ({
    val,
    label: visibilityLabel(val, t),
    hint: visibilityHint(val, t),
  }))
}
