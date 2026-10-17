# Verifier calibration, 2026-10-12 (VER-07 mini calibration)

**Held-out verdict (machine-labelled): FAIL: 2 false accepts of 8 clips labelled corrupted (want 0)**

## Method

- Corpus: internal/calibrate/testdata/passages-en.txt: 22 passages written for this project, so the model cannot read any of them from memory.
- Candidates: 20 good (the passage rendered as is) and 20 corrupted (an edited passage rendered: word-swap, clause-drop, preamble, number-change, name-change, content-word), built with seed 20261011.
- Every candidate is rendered once and verified against the **original** passage, mode `normal` (accept at or above 0.95, reject below 0.85).
- The said guard is bypassed so every take reaches STT; the run fails unless every take was transcribed. Its would-be catch rate is reported separately below.
- Split 60/40, stratified by construction kind and seeded: 24 proposal samples, 16 held out. Thresholds may be proposed on the proposal part only.
- Held-out labels are machine labels from whisper.cpp large-v3-turbo (ggml sha256 1fc70f77), the clip-labelling rule of design D22, not listening: they are weaker evidence than human listening, and a human labelling pass supersedes them. Construction labels are used only to stratify.
- Criteria (VER-07): 0 false accepts on clips labelled corrupted; at most 5% false rejects on clips labelled good. Intervals are Wilson 95%.

## Held-out criteria

| Measure | Count | Rate | Wilson 95% |
|---|---|---|---|
| False accepts (of clips labelled corrupted) | 2 of 8 | 25.0% | 0.07–0.59 |
| False rejects (of clips labelled good) | 0 of 8 | 0.0% | 0.00–0.32 |

## Machine labels (held out)

| Clip | Construction | Machine label | Verifier | Score | Heard | Rejection |
|---|---|---|---|---|---|---|
| s09 | clause-drop | corrupted | rejected | 0.6333 | The bridge at Carrow was built in nine months by a crew of thirty, and it is still standing. | score: score 0.6333 below 0.95 |
| s13 | good | good | accepted | 1.0000 | By the time the telegram reached Lowenstead, the ship it described had already docked safely and its passengers had gone home for supper. |  |
| s14 | word-swap | corrupted | accepted | 1.0000 | In the spring of that year, the council voted to rebuild the old granary, though only four of the nine members had ever seen it full. |  |
| s16 | clause-drop | corrupted | rejected | 0.3704 | When the bell at Saint Aldric's cracked in the frost. | protected-token: deletion: expected "the village raised forty pounds in a week and argued about the new one for a year." / heard "" |
| s17 | name-change | corrupted | rejected | 0.9600 | The market at Boston opened at dawn on Thursdays, and by eight o'clock the best cheese had gone and the gossip had only just begun. | protected-token: substitution: expected "Brindle" / heard "Boston" |
| s18 | good | good | accepted | 1.0000 | The ferry from Castlemore left at seven, but the captain, a quiet man named Ruddick, always waited for the baker's boy before he cast off. |  |
| s19 | number-change | corrupted | rejected | 0.8696 | Ninety lamps were lit along the quay each evening, and the harbormaster counted them twice because the first count was always wrong. | protected-token: substitution: expected "Six" / heard "Ninety" |
| s22 | content-word | corrupted | rejected | 0.9091 | Every old tenant of the corner house complained about the draft, and every one of them stayed longer than they had planned. / Every old tenant of the corner house complained about the draft, and every one of them stayed longer than they had planned. | content-word: the same content-word mismatch was heard twice |
| s23 | good | good | accepted | 1.0000 | The school at Fenwick had one teacher, twenty-two pupils, and a stove that worked only when the wind came from the east. |  |
| s24 | good | good | accepted | 1.0000 | Henrietta Vale learned to sail at sixty, and she said the hardest part was not the knots, but admitting she had been afraid of the water all along. |  |
| s25 | good | good | accepted | 1.0000 | On the third morning of the thaw, Marguerite Ollander walked the eleven miles to Pellbridge with a basket of eggs and a letter she had never meant to send. |  |
| s27 | preamble | corrupted | rejected | 0.7037 | Certainly. Here is the text you asked for. When the bell at Saint Aldric's cracked in the frost, the village raised forty pounds in a week and argued about the new one for a year. | edge-insertion: 8 inserted before, 0 after the text |
| s32 | good | good | accepted | 1.0000 | When the bell at Saint Aldric's cracked in the frost, the village raised forty pounds in a week and argued about the new one for a year. |  |
| s33 | good | good | accepted | 1.0000 | Edwina Marsh wrote to her brother every Sunday, and in thirty years of letters she never once mentioned the farm they had both left behind. |  |
| s37 | good | good | accepted | 1.0000 | The market at Brindle opened at dawn on Thursdays, and by eight o'clock the best cheese had gone and the gossip had only just begun. |  |
| s40 | word-swap | corrupted | accepted | 0.9630 | Professor Hale believed that every map was a kind argument, and he made his students redraw the county from memory before they saw a real one. |  |

## Per-type results (held out)

| Kind | Accepted | Rate | Wilson 95% | Scores |
|---|---|---|---|---|
| good | 8 of 8 | 100.0% | 0.68–1.00 | 1.0000–1.0000 |
| word-swap | 2 of 2 | 100.0% | 0.34–1.00 | 0.9630–1.0000 |
| clause-drop | 0 of 2 | 0.0% | 0.00–0.66 | 0.3704–0.6333 |
| preamble | 0 of 1 | 0.0% | 0.00–0.79 | 0.7037–0.7037 |
| number-change | 0 of 1 | 0.0% | 0.00–0.79 | 0.8696–0.8696 |
| name-change | 0 of 1 | 0.0% | 0.00–0.79 | 0.9600–0.9600 |
| content-word | 0 of 1 | 0.0% | 0.00–0.79 | 0.9091–0.9091 |

## Per-type results (proposal part)

| Kind | Accepted | Rate | Wilson 95% | Scores |
|---|---|---|---|---|
| good | 8 of 12 | 66.7% | 0.39–0.86 | 0.9130–1.0000 |
| word-swap | 1 of 2 | 50.0% | 0.09–0.91 | 0.8333–1.0000 |
| clause-drop | 0 of 2 | 0.0% | 0.00–0.66 | 0.3043–0.7200 |
| preamble | 0 of 2 | 0.0% | 0.00–0.66 | 0.6522–0.8621 |
| number-change | 0 of 2 | 0.0% | 0.00–0.66 | 0.9200–0.9545 |
| name-change | 0 of 2 | 0.0% | 0.00–0.66 | 0.9565–0.9630 |
| content-word | 0 of 2 | 0.0% | 0.00–0.66 | 0.9200–0.9630 |

## Said guard (bypassed; reported separately)

| Kind | Would have caught | Rate | Wilson 95% |
|---|---|---|---|
| good | 0 of 20 | 0.0% | 0.00–0.16 |
| word-swap | 4 of 4 | 100.0% | 0.51–1.00 |
| clause-drop | 4 of 4 | 100.0% | 0.51–1.00 |
| preamble | 3 of 3 | 100.0% | 0.44–1.00 |
| number-change | 3 of 3 | 100.0% | 0.44–1.00 |
| name-change | 3 of 3 | 100.0% | 0.44–1.00 |
| content-word | 3 of 3 | 100.0% | 0.44–1.00 |

## Construction vs machine-label disagreements

None: every held-out label agrees with its construction.

