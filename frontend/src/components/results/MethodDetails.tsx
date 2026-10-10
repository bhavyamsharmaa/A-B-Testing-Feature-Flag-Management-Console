import { fmtCount, safeText } from '../../lib/resultsFormat'
import type { ExperimentResults } from '../../types'

/** The plain-language account of how the numbers were computed (collapsed by default). */
export function MethodDetails({ data }: { data: ExperimentResults }) {
  const t = data.thresholds
  // API notes that contain a non-value word are left out rather than shown as a dash
  const technical = data.method.filter((m) => safeText(m) === m)
  return (
    <details className="rounded-xl border border-white/10 bg-white/[0.02] px-4 py-3 text-sm text-zinc-200" data-testid="method">
      <summary className="cursor-pointer select-none font-medium text-zinc-100">How these numbers are computed</summary>
      <div className="mt-3 space-y-3 leading-relaxed">
        <div>
          <h4 className="font-medium text-zinc-100">Who is counted</h4>
          <p>
            Each person (subject) is counted once, in the variation they were first shown. Only what they did <em>after</em> they were shown it, and
            while the experiment was running, counts. Events from people who were never shown a variation are left out, and the warnings above say how many.
          </p>
        </div>
        <div>
          <h4 className="font-medium text-zinc-100">What is measured</h4>
          <p>
            <strong>Conversion</strong>: did the subject do it at least once (yes or no). <strong>Count per subject</strong>: how many times.{' '}
            <strong>Value per subject</strong>: the total of the event values, with 0 for people who did nothing.
          </p>
        </div>
        <div>
          <h4 className="font-medium text-zinc-100">The comparison</h4>
          <p>
            Every variation is compared with the control, which is the flag&apos;s first variation. For conversions the test compares two rates; for counts and
            values it compares two averages (Welch&apos;s t-test, which does not assume equal spread). The p-value is the chance of seeing a difference at least this
            large if there were really none. A small p-value is not proof, and a large one is not proof of no effect.
          </p>
        </div>
        <div>
          <h4 className="font-medium text-zinc-100">Several comparisons</h4>
          <p>
            Comparing more than one variation with the control gives more chances for a fluke. To keep the overall false-alarm rate at 5%, each comparison is tested
            more strictly (Bonferroni: 5% divided by the number of comparisons). That is why the adjusted p-value is larger and the intervals are wider than 95%.
          </p>
        </div>
        <div>
          <h4 className="font-medium text-zinc-100">Why not to stop early</h4>
          <p>
            These p-values assume you decided the sample size in advance. Checking repeatedly and stopping when something looks significant makes false alarms
            far more common than 5%. Use the numbers while the experiment runs to check that data is arriving, not to decide.
          </p>
        </div>
        <div>
          <h4 className="font-medium text-zinc-100">When no verdict is shown</h4>
          <p>
            Until each variation has at least {fmtCount(t.minExposuresPerVariation)} exposures (and a conversion metric at least {fmtCount(t.minConversionsTotal)}{' '}
            conversions in total) only the raw numbers are shown. If the split of subjects between variations differs from the rollout by more than chance
            (sample ratio mismatch, p &lt; {t.srmPValue}), every number is marked not reliable.
          </p>
        </div>
        {technical.length > 0 && (
          <div>
            <h4 className="font-medium text-zinc-100">Technical notes (from the API)</h4>
            <ul className="list-disc space-y-1 pl-5 text-zinc-300">
              {technical.map((m, i) => (
                <li key={i}>{m}</li>
              ))}
            </ul>
          </div>
        )}
      </div>
    </details>
  )
}
