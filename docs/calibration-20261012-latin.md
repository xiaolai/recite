# Verifier calibration, 2026-10-12 (VER-11 full calibration, latin)

**Held-out verdict (machine-labelled): FAIL: 1 false accepts of 24 clips labelled corrupted (want 0); 9 false rejects of 24 clips labelled good (want at most 5%)**

## Method

- Corpus: internal/calibrate/testdata/passages-en.txt then passages-en-more.txt: 60 English passages written for this project, so the model cannot read any of them from memory.
- Candidates: 60 good (the passage rendered as is) and 60 corrupted (an edited passage rendered: word-swap, clause-drop, preamble, number-change, name-change, content-word), built with seed 20261011.
- Every candidate is rendered once and verified against the **original** passage, mode `normal` (accept at or above 0.95, reject below 0.85).
- The said guard is bypassed so every take reaches STT; the run fails unless every take was transcribed. Its would-be catch rate is reported separately below.
- Split 60/40, stratified by construction kind and seeded: 72 proposal samples, 48 held out. Thresholds may be proposed on the proposal part only.
- Held-out labels are machine labels from whisper.cpp large-v3-turbo (ggml sha256 1fc70f77), the clip-labelling rule of design D22, not listening: they are weaker evidence than human listening, and a human labelling pass supersedes them. Construction labels are used only to stratify.
- Criteria (VER-07): 0 false accepts on clips labelled corrupted; at most 5% false rejects on clips labelled good. Intervals are Wilson 95%.

## Held-out criteria

| Measure | Count | Rate | Wilson 95% |
|---|---|---|---|
| False accepts (of clips labelled corrupted) | 1 of 24 | 4.2% | 0.01–0.20 |
| False rejects (of clips labelled good) | 9 of 24 | 37.5% | 0.21–0.57 |

## Machine labels (held out)

| Clip | Construction | Machine label | Verifier | Score | Heard | Rejection |
|---|---|---|---|---|---|---|
| s003 | number-change | corrupted | rejected | 0.9091 | Rosalind Thackeray grew roses for the county show, and she won second prize sixty years running, always behind the same quiet neighbor. | protected-token: substitution: expected "twelve" / heard "sixty" |
| s005 | good | good | accepted | 1.0000 | The postbox at Ditchley Corner was emptied twice a day, though nobody could remember the last time anyone had actually posted a letter in it. |  |
| s006 | good | good | accepted | 1.0000 | Jonas Merriwether bought the derelict cinema on Station Road for five hundred pounds, and for a decade he showed the same three films in rotation. |  |
| s014 | good | good | rejected | 0.8750 | Hester Lindquist ran the telephone exchange at Grinley for 26 years, as she knew every secret in the valley and kept every one. | protected-token: substitution: expected "Lindqvist" / heard "Lindquist" |
| s018 | content-word | corrupted | rejected | 0.9583 | The ferryman at Rookwater charged one penny to cross the river and two to come back, because, he said, the return silver was uphill. / The ferryman at Rookwater charged one penny to cross the river and two to come back, because, he said, the return silver was uphill. | content-word: the same content-word mismatch was heard twice |
| s019 | word-swap | corrupted | rejected | 0.9286 | The night train to Harrowgate stopped at small forest stations, but the guard, a cheerful man named Fennick, announced each one as if it were a capital city. | protected-token: substitution: expected "four small" / heard "small forest" |
| s020 | good | good | accepted | 1.0000 | Gideon Farrow kept a diary for fifty years, and when his grandson finally read it, he found that every single entry began with the weather. |  |
| s022 | name-change | corrupted | rejected | 0.9600 | The mill at Madison ran day and night through the autumn, and the miller's daughter learned to sleep through a noise that kept visitors awake. | protected-token: substitution: expected "Dunmere" / heard "Madison" |
| s023 | good | good | rejected | 0.9615 | Every autumn, the geese flew over Hollins Moor in their thousands, and the shepherd, a man named Corwin, stopped work to count them from the ridge. | protected-token: substitution: expected "Corwen," / heard "Corwin," |
| s024 | word-swap | corrupted | rejected | 0.9167 | Mabel Fairweather taught the village choir for forty years, and she said the secret of singing good was to listen harder than you sang. / Mabel Fairweather taught the village choir for forty years, and she said the secret of singing good was to listen harder than you sang. | content-word: the same content-word mismatch was heard twice |
| s027 | good | good | rejected | 0.9615 | The village of Thornley had one telephone, kept in the shop, and when it rang, the shopkeeper ran into the street to shout the caller's name. | protected-token: substitution: expected "Thornleigh" / heard "Thornley" |
| s028 | good | good | rejected | 0.9545 | The lighthouse keeper at Gannet Point, a widower called Osric Penhale, painted the stairs a different color every spring for twenty years. / The lighthouse keeper at Gannet Point, a widower called Osric Penhale, painted the stairs a different color every spring for twenty years. | content-word: the same content-word mismatch was heard twice |
| s034 | word-swap | corrupted | rejected | 0.9130 | Barnaby Quill wrote the parish newsletter for eleven years, and in all that time he printed one only correction, which was itself mistaken. | protected-token: substitution: expected "only one" / heard "one only" |
| s040 | preamble | corrupted | rejected | 0.8519 | Here is the passage. The old mill race at Stowford still ran, though the mill had gone, and the children floated paper boats down it to a pool beneath the willows. | edge-insertion: 4 inserted before, 0 after the text |
| s042 | good | good | accepted | 1.0000 | Wilhelmina Strand kept bees on the roof of the post office, and the postmaster claimed the letters arrived sweeter in the summer months. |  |
| s044 | good | good | rejected | 0.9200 | The librarian at Ashcombe lent books for two weeks, but the librarian, Mr. Price, rarely asked for them back before the end of the season. | protected-token: substitution: expected "Pryce," / heard "Price," |
| s049 | number-change | corrupted | rejected | 0.8696 | Eight lamps were lit along the quay each evening, and the harbormaster counted them twice because the first count was always wrong. | protected-token: substitution: expected "Six" / heard "Eight" |
| s050 | clause-drop | corrupted | rejected | 0.5909 | Rosalind Thackeray grew roses for the county show, always behind the same quiet neighbor. | protected-token: deletion: expected "and she won second prize twelve years running," / heard "" |
| s051 | good | good | rejected | 0.9259 | Percival Dunn ran the lending library at Wolcombe for thirty years, and he could tell you who had borrowed any book by the smell of its pages. | protected-token: substitution: expected "Dunne" / heard "Dunn" |
| s052 | good | good | accepted | 1.0000 | Barnaby Quill wrote the parish newsletter for eleven years, and in all that time he printed only one correction, which was itself mistaken. |  |
| s053 | clause-drop | corrupted | rejected | 0.4444 | The clock on the town hall at Penmorrow ran ten minutes slow. | score: score 0.4444 below 0.95 |
| s055 | preamble | corrupted | rejected | 0.8519 | Here is the passage. Professor Hale believed that every map was a kind of argument, and he made his students redraw the county from memory before they saw a real one. | edge-insertion: 4 inserted before, 0 after the text |
| s059 | content-word | corrupted | rejected | 0.9615 | Ivy Coulthard distant her three dogs along the cliff path every morning, and the dogs knew the way so well that she sometimes simply followed them. / Ivy Coulthard distant her three dogs along the cliff path every morning, and the dogs knew the way so well that she sometimes simply followed them. | content-word: the same content-word mismatch was heard twice |
| s062 | good | good | accepted | 1.0000 | The ferry from Castlemore left at seven, but the captain, a quiet man named Ruddick, always waited for the baker's boy before he cast off. |  |
| s065 | preamble | corrupted | rejected | 0.6522 | Certainly. Here is the text you asked for. The road over Ravenscar Pass closed every December, and the four farms beyond it laid in flour, candles, and patience until the spring. | edge-insertion: 8 inserted before, 0 after the text |
| s067 | number-change | corrupted | rejected | 0.9630 | When the bell at Saint Aldric's cracked in the frost, the village raised forty pounds in a week and argued about the new two for a year. | protected-token: substitution: expected "one" / heard "two" |
| s068 | name-change | corrupted | rejected | 0.7083 | All of this is Tolly and Linden Row baked nine loaves every Madison, though she lived alone and never once explained who ate the other eight. | protected-token: substitution: expected "Old Mrs Tolley at" / heard "All of this is Tolly and" |
| s070 | content-word | corrupted | accepted | 1.0000 | The market at Brindle opened at dawn on Thursdays, and by eight o'clock the best cheese had gone and the gossip had only just begun. |  |
| s072 | good | good | accepted | 1.0000 | Agatha Moorcroft swam in the lake at Elderwater every morning of the year, and in January she broke the ice herself with a garden spade. |  |
| s073 | preamble | corrupted | rejected | 0.8400 | Sure, here it is. The market at Brindle opened at dawn on Thursdays, and by eight o'clock the best cheese had gone and the gossip had only just begun. | edge-insertion: 4 inserted before, 0 after the text |
| s074 | good | good | accepted | 1.0000 | Ivy Coulthard walked her three dogs along the cliff path every morning, and the dogs knew the way so well that she sometimes simply followed them. |  |
| s082 | word-swap | corrupted | rejected | 0.8636 | The lighthouse keeper at Gannet Point, a widower called Osric Penhale, painted the stairs a different color every spring for years twenty. | protected-token: substitution: expected "twenty years." / heard "years twenty." |
| s086 | good | good | rejected | 0.9091 | The harbor at Polgara froze only once in living memory, and the fishermen still argue about whether anybody really walked across it. | protected-token: substitution: expected "Polgarra" / heard "Polgara" |
| s087 | number-change | corrupted | rejected | 0.9600 | Gideon Farrow kept a diary for two years, and when his grandson finally read it, he found that every single entry began with the weather. | protected-token: substitution: expected "fifty" / heard "two" |
| s088 | clause-drop | corrupted | rejected | 0.4167 | The tide at Saltmarsh came in faster than strangers expected. | protected-token: deletion: expected "and the coastguard rescued at least two picnics a summer from the rising water." / heard "" |
| s090 | good | good | accepted | 1.0000 | Lucian Abernathy repaired violins in a workshop above the chemist, and he claimed that every instrument told him what was wrong with it. |  |
| s093 | good | good | accepted | 1.0000 | The old mill race at Stowford still ran, though the mill had gone, and the children floated paper boats down it to a pool beneath the willows. |  |
| s096 | name-change | corrupted | rejected | 0.9091 | The lighthouse keeper at Jefferson Point, a widower called Osric Penhale, painted the stairs a different color every spring for twenty years. | protected-token: substitution: expected "Gannet" / heard "Jefferson" |
| s101 | good | good | accepted | 1.0000 | Clementine Ashdown learned Latin at seventy, and by eighty she was correcting the inscriptions on half the gravestones in the churchyard. |  |
| s103 | good | good | rejected | 0.9545 | Rosalind Thackeray grew roses for the county show, and she won second prize twelve years running, always behind the same quiet neighbor. / Rosalind Thackeray grew roses for the county show, and she won second prize twelve years running, always behind the same quiet neighbor. | content-word: the same content-word mismatch was heard twice |
| s104 | content-word | corrupted | rejected | 0.9630 | The canal boat, called the Patient Heron, carried coal to Wexford Basin for half a century, and it was never once late, though it was always early. / The canal boat, called the Patient Heron, carried coal to Wexford Basin for half a century, and it was never once late, though it was always early. | content-word: the same content-word mismatch was heard twice |
| s106 | good | good | accepted | 1.0000 | At the end of every term, the headmistress of Coldharbour School read out the names of all sixty pupils, and she never needed to look at a list. |  |
| s108 | clause-drop | corrupted | rejected | 0.4167 | The stationmaster at Little Hembury grew tomatoes along the platform. | protected-token: deletion: expected "and passengers on the slow train were allowed to pick one each in August." / heard "" |
| s112 | good | good | accepted | 1.0000 | The orchard behind the Halloran's house produced more pears than apples, which surprised everyone except the woman who had planted it. |  |
| s113 | good | good | accepted | 1.0000 | Winifred Hale made jam from the brambles along the railway line, and she sold every jar to the driver of the nine o'clock goods train. |  |
| s115 | good | good | accepted | 0.9565 | A school at Fenwick had one teacher, twenty-two pupils, and a stove that worked only when the wind came from the east. |  |
| s117 | good | good | rejected | 0.9545 | Every new tenant of the corner house complained about the draft, and every one of them stayed longer than they had planned. / Every new tenant of the corner house complained about the draft, and every one of them stayed longer than they had planned. | content-word: the same content-word mismatch was heard twice |
| s118 | name-change | corrupted | rejected | 0.9630 | The bakery on Fuller Jefferson opened at five, but the queue began forming at half past four because the first tray of buns was always the best. | protected-token: substitution: expected "Street" / heard "Jefferson" |

## Per-type results (held out)

| Kind | Accepted | Rate | Wilson 95% | Scores |
|---|---|---|---|---|
| good | 15 of 24 | 62.5% | 0.43–0.79 | 0.8750–1.0000 |
| word-swap | 0 of 4 | 0.0% | 0.00–0.49 | 0.8636–0.9286 |
| clause-drop | 0 of 4 | 0.0% | 0.00–0.49 | 0.4167–0.5909 |
| preamble | 0 of 4 | 0.0% | 0.00–0.49 | 0.6522–0.8519 |
| number-change | 0 of 4 | 0.0% | 0.00–0.49 | 0.8696–0.9630 |
| name-change | 0 of 4 | 0.0% | 0.00–0.49 | 0.7083–0.9630 |
| content-word | 1 of 4 | 25.0% | 0.05–0.70 | 0.9583–1.0000 |

## Per-type results (proposal part)

| Kind | Accepted | Rate | Wilson 95% | Scores |
|---|---|---|---|---|
| good | 27 of 36 | 75.0% | 0.59–0.86 | 0.8333–1.0000 |
| word-swap | 1 of 6 | 16.7% | 0.03–0.56 | 0.7500–1.0000 |
| clause-drop | 1 of 6 | 16.7% | 0.03–0.56 | 0.1379–0.9583 |
| preamble | 0 of 6 | 0.0% | 0.00–0.39 | 0.6667–0.8462 |
| number-change | 0 of 6 | 0.0% | 0.00–0.39 | 0.9565–0.9655 |
| name-change | 0 of 6 | 0.0% | 0.00–0.39 | 0.8696–0.9615 |
| content-word | 0 of 6 | 0.0% | 0.00–0.39 | 0.9130–0.9615 |

## Said guard (bypassed; reported separately)

| Kind | Would have caught | Rate | Wilson 95% |
|---|---|---|---|
| good | 0 of 60 | 0.0% | 0.00–0.06 |
| word-swap | 10 of 10 | 100.0% | 0.72–1.00 |
| clause-drop | 10 of 10 | 100.0% | 0.72–1.00 |
| preamble | 10 of 10 | 100.0% | 0.72–1.00 |
| number-change | 10 of 10 | 100.0% | 0.72–1.00 |
| name-change | 10 of 10 | 100.0% | 0.72–1.00 |
| content-word | 10 of 10 | 100.0% | 0.72–1.00 |

## Construction vs machine-label disagreements

None: every held-out label agrees with its construction.

