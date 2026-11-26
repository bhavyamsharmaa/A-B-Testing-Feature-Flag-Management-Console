import { ApiError } from '../api/client'
import { createFlag, getFlag, setFlagServed } from '../api/flags'
import type { Flag } from '../types'
import { DISCOUNT_PARTS, splitDiscount, type DiscountBanner, type DiscountPart } from './bannerDiscount'

async function createPart(env: string, bannerKey: string, part: DiscountPart): Promise<Flag> {
  const { key, label, variations } = DISCOUNT_PARTS[part]
  try {
    return await createFlag(env, {
      key: key(bannerKey),
      name: `${bannerKey} discount (${label})`,
      description: `Discount value of the banner ${bannerKey}.`,
      variationType: 'number',
      variations: [...variations],
    })
  } catch (err) {
    if (err instanceof ApiError && err.status === 409) return getFlag(env, key(bannerKey))
    throw err
  }
}

/**
 * Makes `percent` the live discount in `env`, creating the hidden flags the
 * first time. Every server answer is passed to `onFlag`, also when the other
 * half fails; the first failure is then thrown.
 */
export async function saveDiscount(
  env: string,
  bannerKey: string,
  current: DiscountBanner,
  percent: number,
  onFlag: (part: DiscountPart, flag: Flag) => void,
): Promise<void> {
  const ids = splitDiscount(percent)
  // Sent together so the website sees both halves at almost the same moment.
  const results = await Promise.allSettled(
    (['tens', 'ones'] as const).map(async (part) => {
      let flag = current[part]
      if (!flag) {
        flag = await createPart(env, bannerKey, part)
        onFlag(part, flag)
      }
      if (flag.config.enabled && flag.config.fallthroughVariationId === ids[part]) return
      onFlag(part, await setFlagServed(env, flag.key, ids[part]))
    }),
  )
  const failed = results.find((r) => r.status === 'rejected')
  if (failed) throw failed.reason
}
