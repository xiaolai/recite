# Verifier calibration, 2026-10-12 (VER-11 full calibration, han)

**Held-out verdict (machine-labelled): FAIL: 21 false rejects of 24 clips labelled good (want at most 5%)**

## Method

- Corpus: internal/calibrate/testdata/passages-zh.txt: 62 Chinese passages written for this project, so the model cannot read any of them from memory.
- Candidates: 60 good (the passage rendered as is) and 60 corrupted (an edited passage rendered: word-swap, clause-drop, preamble, number-change, name-change, content-word), built with seed 20261011.
- Every candidate is rendered once and verified against the **original** passage, mode `normal` (accept at or above 0.95, reject below 0.85).
- The said guard is bypassed so every take reaches STT; the run fails unless every take was transcribed. Its would-be catch rate is reported separately below.
- Split 60/40, stratified by construction kind and seeded: 72 proposal samples, 48 held out. Thresholds may be proposed on the proposal part only.
- Held-out labels are machine labels from whisper.cpp large-v3-turbo (ggml sha256 1fc70f77), the clip-labelling rule of design D22, not listening: they are weaker evidence than human listening, and a human labelling pass supersedes them. Construction labels are used only to stratify.
- Criteria (VER-07): 0 false accepts on clips labelled corrupted; at most 5% false rejects on clips labelled good. Intervals are Wilson 95%.

## Held-out criteria

| Measure | Count | Rate | Wilson 95% |
|---|---|---|---|
| False accepts (of clips labelled corrupted) | 0 of 24 | 0.0% | 0.00–0.14 |
| False rejects (of clips labelled good) | 21 of 24 | 87.5% | 0.69–0.96 |

## Machine labels (held out)

| Clip | Construction | Machine label | Verifier | Score | Heard | Rejection |
|---|---|---|---|---|---|---|
| s005 | good | good | rejected | 0.9737 | 童花巷尽头有一棵老槐树，树下的石凳坐过三代人。许婉晴小时候在那里学会了下棋，后来又在那里教会了自己的孙子。孙子第一次赢她的时候，她高兴了一整天。 / 童花巷尽头有一棵老槐树，树下的石凳坐过三代人。许婉晴小时候在那里学会了下棋，后来又在那里教会了自己的孙子。孙子第一次赢她的时候，她高兴了一整天。 | content-word: the same content-word mismatch was heard twice |
| s006 | good | good | rejected | 0.9351 | 前半下做向导的第一年，带了七十个客人上山，没有一个迷路。第二年他带的人少了，因为客人们都说跟他走太累，他一点也不在乎，说山不会因为人少就变矮。 / 前半下做向导的第一年，带了七十个客人上山，没有一个迷路。第二年他带的人少了，因为客人们都说跟他走太累，他一点也不在乎，说山不会因为人少就变矮。 | content-word: the same content-word mismatch was heard twice |
| s010 | number-change | corrupted | rejected | 0.8732 | 布尔叶湖的冬天很长，渔民们在冰上凿洞钓鱼，故常刚十天能钓三十多条。他说，诀窍不在鱼饵，在于能不能坐得住。年轻人跟他学，往往坐不到一个钟头就走了。 | protected-token: substitution: expected "顾长庚一" / heard "故常刚十" |
| s016 | good | good | accepted | 1.0000 | 白沙渡的集市每周四天亮开张，不到八点，最好的奶酪就卖完了。而街坊之间的闲话那时候才刚刚开始，一直要说到中午，比奶酪卖得还快，也比奶酪传得更远。 |  |
| s020 | good | good | rejected | 0.9867 | 孟东生年轻时在柳湾村教过两年书，后来去了城里。四十年后，他回到村里，发现当年的学生都已经当了爷爷奶奶。他们却还是站起来，叫了他一声老师。他当场红了眼眶。 / 孟东生年轻时在柳湾村教过两年书，后来去了城里。四十年后，他回到村里，发现当年的学生都已经当了爷爷奶奶。他们却还是站起来，叫了他一声老师。他当场红了眼眶。 | content-word: the same content-word mismatch was heard twice |
| s021 | word-swap | corrupted | rejected | 0.9600 | 孟东生年轻时在柳湾村教过两年书，后来去了城里。四十年后，他回到村里，发现当年的学生都已经当了爷爷奶奶，他们却还是站起来，叫了他一声老师，他当场红眼眶了。 / 孟东生年轻时在柳湾村教过两年书，后来去了城里。四十年后，他回到村里，发现当年的学生都已经当了爷爷奶奶，他们却还是站起来，叫了他一声老师，他当场红眼眶了。 | content-word: the same content-word mismatch was heard twice |
| s022 | good | good | rejected | 0.9342 | 盘家三代人都在洛厄湖的同一段水面打鱼，可是关于一天里什么时候下网最好，他们从来没有意见一致过。韩书岩至今还在争，尽管他早就不打鱼了，只是喜欢争。 / 盘家三代人都在洛厄湖的同一段水面打鱼，可是关于一天里什么时候下网最好，他们从来没有意见一致过。韩书岩至今还在争，尽管他早就不打鱼了，只是喜欢争。 | content-word: the same content-word mismatch was heard twice |
| s024 | good | good | rejected | 0.8356 | 沈七五寄两本账，一本记店里的进出，一本记谁欠了钱又不肯承认。他对这两本账都不完全放心，晚上常常翻来覆去地核对，直到灯油烧完，才肯合上账本去睡觉。 | score: score 0.8356 below 0.95 |
| s025 | good | good | rejected | 0.9722 | 何小满老师认为，每一张地图都是一种主张，所以他要求学生先凭记忆画出整个云西县，然后才能看真正的地图。画得最不像的那张，常常最能说明画图的人住在哪里。 / 何小满老师认为，每一张地图都是一种主张，所以他要求学生先凭记忆画出整个云西县，然后才能看真正的地图。画得最不像的那张，常常最能说明画图的人住在哪里。 | content-word: the same content-word mismatch was heard twice |
| s027 | preamble | corrupted | rejected | 0.8406 | 当然，内容如下：赵元叔的驴车每天往返北岭和青石镇两次。驴子认得路，有一回它在车上睡着了，驴子照样把他拉到了家门口，还在门口叫了两声，好像在提醒他该下车了。 | edge-insertion: 6 inserted before, 0 after the text |
| s028 | word-swap | corrupted | rejected | 0.9213 | 赵云叔七十岁那年，卖掉了驴车，买了一辆三轮车。可是他骑得很慢，比驴子还慢。镇上的人都说，他其实是舍不得那头驴，因为他每天绕海市路去看它一眼，有时候还给它带一根胡萝卜。 / 赵云叔七十岁那年，卖掉了驴车，买了一辆三轮车。可是他骑得很慢，比驴子还慢。镇上的人都说，他其实是舍不得那头驴，因为他每天绕海市路去看它一眼，有时候还给它带一根胡萝卜。 | content-word: the same content-word mismatch was heard twice |
| s029 | name-change | corrupted | rejected | 0.9610 | 陈守义当了八年村长，最得意的事不是修路，而是说服了全村人在河边种一百棵柳树。现在那里夏天是最凉快的地方，老人们下棋，孩子们捉知了，谁也不想回家。 / 陈守义当了八年村长，最得意的事不是修路，而是说服了全村人在河边种一百棵柳树。现在那里夏天是最凉快的地方，老人们下棋，孩子们捉知了，谁也不想回家。 | content-word: the same content-word mismatch was heard twice |
| s036 | good | good | rejected | 0.9275 | 磨燕湖边的魔坊，整个秋天日夜不停。魔坊主的女儿学会了在吵闹声里安静地睡觉，而来做客的人往往一夜都睡不着。等到魔坊停了，她反而醒过来，以为出了什么事。 / 磨燕湖边的魔坊，整个秋天日夜不停。魔坊主的女儿学会了在吵闹声里安静地睡觉，而来做客的人往往一夜都睡不着。等到魔坊停了，她反而醒过来，以为出了什么事。 | content-word: the same content-word mismatch was heard twice |
| s037 | good | good | rejected | 0.9867 | 每一个搬进拐角那栋房子的租客都抱有漏风，可是他们每一个人住的时间都比自己原来打算的要长。方立春一住就是七年，搬走那天，他还特意回来关了窗户。 / 每一个搬进拐角那栋房子的租客都抱有漏风，可是他们每一个人住的时间都比自己原来打算的要长。方立春一住就是七年，搬走那天，他还特意回来关了窗户。 | content-word: the same content-word mismatch was heard twice |
| s042 | number-change | corrupted | rejected | 0.8941 | 唐之秋每个星期天给弟弟写一封信，一百年里写了上千封，却从来没有提起他们一起离开的那座农场，好像它从来不存在。弟弟的回信也一样，只说天气和孩子。 | protected-token: substitution: expected "三十" / heard "一百" |
| s043 | number-change | corrupted | rejected | 0.9178 | 叶天兰沿着北边的院墙种了十台冬青。此后五十个冬天里，从大路上望过去，那是唯一能看见的一点绿色。路人都把它叫做叶家墙，连邮差送信也认这个名字。 | protected-token: substitution: expected "一排" / heard "十台" |
| s045 | number-change | corrupted | rejected | 0.9589 | 石门街的理发店开了三十年，换了三个师傅，镜子却一直是原来那面。老顾客说，照这面镜子，总觉得自己年轻了几岁。所以镜子上那道裂缝，谁也不许师傅去修。 | protected-token: substitution: expected "五十" / heard "三十" |
| s046 | clause-drop | corrupted | rejected | 0.9157 | 方桥镇的小火车站，每天只停两班车，早上一班，晚上一班。等车的人越少，他就越能记住每一张脸。有时候谁没来，他会一直惦记到第二天，生怕那个人出了什么事。 / 方桥镇的小火车站，每天只停两班车，早上一班，晚上一班。等车的人越少，他就越能记住每一张脸。有时候谁没来，他会一直惦记到第二天，生怕那个人出了什么事。 | content-word: the same content-word mismatch was heard twice |
| s048 | good | good | rejected | 0.9615 | 和小蛮的课堂上有一条规矩：谁先举手谁先说，可是说错了不罚，说对了也不奖。十五年来，这条规矩从来没改过。他说，这样孩子们才敢说心里真正想的话，哪怕那些话并不好听。 / 和小蛮的课堂上有一条规矩：谁先举手谁先说，可是说错了不罚，说对了也不奖。十五年来，这条规矩从来没改过。他说，这样孩子们才敢说心里真正想的话，哪怕那些话并不好听。 | content-word: the same content-word mismatch was heard twice |
| s049 | good | good | rejected | 0.9583 | 曲婉婷在白沙渡开了一间小书店，书店里只卖旧书，每本都是她自己读过的。她说没读过的书不好意思卖给别人，所以书店里的书每年只多出几十本，她却觉得刚刚好。 / 曲婉婷在白沙渡开了一间小书店，书店里只卖旧书，每本都是她自己读过的。她说没读过的书不好意思卖给别人，所以书店里的书每年只多出几十本，她却觉得刚刚好。 | content-word: the same content-word mismatch was heard twice |
| s055 | clause-drop | corrupted | rejected | 0.7805 | 夜听兰收藏旧地图，最老的一张挂的是两百年前的石门街。街名大多改了，只有一口井的位置和今天一模一样，好像在对时间。 | protected-token: deletion: expected "她常拿着地图去井边站一会儿" / heard "" |
| s057 | word-swap | corrupted | rejected | 0.9740 | 周木元退休前的最后一趟船，载了二十个学生过河。学生们在船上唱歌，他说那是他撑了三十年船，听过最好听的一次。靠岸以后，他一个人在船上坐了很久。 / 周木元退休前的最后一趟船，载了二十个学生过河。学生们在船上唱歌，他说那是他撑了三十年船，听过最好听的一次。靠岸以后，他一个人在船上坐了很久。 | content-word: the same content-word mismatch was heard twice |
| s060 | good | good | accepted | 1.0000 | 吴启明修了四十年自行车，他的铺子在石门街拐角，门口挂着一块旧木牌，上面写着：能修的都修，不能修的也先看看。很多人推着车来，其实只是想找他聊几句。 |  |
| s063 | content-word | corrupted | rejected | 0.9333 | 宋怀简六十岁才学划船。他说，最难的不是打结，而是承认自己这一辈子其实一直都怕水。说完，他自己先笑了起来。第二年冬天，他已经能一个人划到湖心去了。 / 宋怀简六十岁才学划船。他说，最难的不是打结，而是承认自己这一辈子其实一直都怕水。说完，他自己先笑了起来。第二年冬天，他已经能一个人划到湖心去了。 | content-word: the same content-word mismatch was heard twice |
| s067 | good | good | rejected | 0.9333 | 宋怀景六十岁才学划船，他说最难的不是打桨，而是承认自己这一辈子其实一直都怕水。说完他自己先笑了起来。第二年夏天，他已经能一个人划到湖心去了。 / 宋怀景六十岁才学划船，他说最难的不是打桨，而是承认自己这一辈子其实一直都怕水。说完他自己先笑了起来。第二年夏天，他已经能一个人划到湖心去了。 | content-word: the same content-word mismatch was heard twice |
| s068 | good | good | rejected | 0.9873 | 种远山退休以后，在西坡种菜。他种的南瓜一个比一个大，最大的那个两个人才抬得动。被送到县里去参加了比赛。回来的时候，南瓜没了，只带回一张奖状。他把奖状贴在了床头。 / 种远山退休以后，在西坡种菜。他种的南瓜一个比一个大，最大的那个两个人才抬得动。被送到县里去参加了比赛。回来的时候，南瓜没了，只带回一张奖状。他把奖状贴在了床头。 | content-word: the same content-word mismatch was heard twice |
| s069 | clause-drop | corrupted | rejected | 0.8488 | 游戏的石板路一共有九百块石板，和小满带着学生一块儿一块儿数过，数到最后少了一块。大家找了一个下午也没找到，是有人把它搬回家垫了水缸。 | protected-token: deletion: expected "第二天才发现" / heard "" |
| s070 | name-change | corrupted | rejected | 0.9275 | 顾长庚年轻的时候在西坡撑船，后来改行卖鱼具。他说卖鱼具比撑船赚得多，可是远远没有撑船快活。所以每到星期天，他还是会借一条船，在河上撑一个上午。 / 顾长庚年轻的时候在西坡撑船，后来改行卖鱼具。他说卖鱼具比撑船赚得多，可是远远没有撑船快活。所以每到星期天，他还是会借一条船，在河上撑一个上午。 | content-word: the same content-word mismatch was heard twice |
| s082 | good | good | accepted | 1.0000 | 方立春，当了八年村长，最得意的事不是修路，而是说服了全村人在河边种一百棵柳树。现在那里夏天是最凉快的地方，老人们下棋，孩子们捉知了，谁也不想回家。 |  |
| s085 | good | good | rejected | 0.9412 | 丰桥镇的老周在一场大双里裂了。镇上的人一个星期就凑了四十万，然后为了新中该挂在哪里，足足争论了一年。最后，新中挂回了原来的地方，谁也没有赢。 / 丰桥镇的老周在一场大双里裂了。镇上的人一个星期就凑了四十万，然后为了新中该挂在哪里，足足争论了一年。最后，新中挂回了原来的地方，谁也没有赢。 | content-word: the same content-word mismatch was heard twice |
| s086 | preamble | corrupted | rejected | 0.8611 | 当然，内容如下。何湘南老师认为，每一张地图都是一种主张，所以他要求学生先凭记忆画出整个云西县，然后才能看真正的地图。画得最不像的那张，常常最能说明画图的人住在哪里。 | edge-insertion: 6 inserted before, 0 after the text |
| s088 | good | good | rejected | 0.9759 | 枫桥镇的小火车站每天只停两班车，早上一班，晚上一班。站长沈青无说，等车的人越少，他就越能记住每一张脸。有时候谁没来，他会一直惦记到第二天，生怕那个人出了什么事儿。 / 枫桥镇的小火车站每天只停两班车，早上一班，晚上一班。站长沈青无说，等车的人越少，他就越能记住每一张脸。有时候谁没来，他会一直惦记到第二天，生怕那个人出了什么事儿。 | content-word: the same content-word mismatch was heard twice |
| s089 | content-word | corrupted | rejected | 0.9452 | 叶天兰沿着北边的院墙种了一排冬青，此后五十夏天里，从大路上望过去，那是唯一能看见的一点绿色。路人都把它叫做叶家墙，连邮差送信也认这个名字。 / 叶天兰沿着北边的院墙种了一排冬青，此后五十夏天里，从大路上望过去，那是唯一能看见的一点绿色。路人都把它叫做叶家墙，连邮差送信也认这个名字。 | content-word: the same content-word mismatch was heard twice |
| s090 | preamble | corrupted | rejected | 0.8675 | 好的，下面是这段文字。枫桥镇的小火车站每天只停两班车，早上一班，晚上一班。站长沈清无说，等车的人越少，他就越能记住每一张脸。有时候谁没来，他会一直惦记到第二天，生怕那个人出了什么事。 | edge-insertion: 9 inserted before, 0 after the text |
| s093 | preamble | corrupted | rejected | 0.8750 | 那天起，我来独居的话。南城的茶馆早上五点开门，第一批客人总是那几位老先生。他们坐同一张桌子，喝同一种茶，聊的却每天都不一样。有一天少了一位，其余的人整个上午都没说话。 | edge-insertion: 9 inserted before, 0 after the text |
| s098 | good | good | rejected | 0.9870 | 丰桥镇的戏台每年秋天唱五天戏，前两天的观众总是很少。到了最后一天，连隔壁镇的人都搬着板凳来占位子。台上唱的是什么，反倒没几个人在意，大家要的只是那份热闹。 / 丰桥镇的戏台每年秋天唱五天戏，前两天的观众总是很少。到了最后一天，连隔壁镇的人都搬着板凳来占位子。台上唱的是什么，反倒没几个人在意，大家要的只是那份热闹。 | content-word: the same content-word mismatch was heard twice |
| s099 | good | good | rejected | 0.9420 | 赵云说的驴车每天往返北岭和青石镇两次，驴子认得路。有一回它在车上睡着了，驴子照样把他拉到了家门口，还在门口叫了两声，好像在提醒他该下车了。 / 赵云说的驴车每天往返北岭和青石镇两次，驴子认得路。有一回它在车上睡着了，驴子照样把他拉到了家门口，还在门口叫了两声，好像在提醒他该下车了。 | content-word: the same content-word mismatch was heard twice |
| s101 | good | good | rejected | 0.9756 | 同发巷每年腊月都要摆一次长桌宴，二十多户人家各出一道菜，苏念安家年年做红烧肉，有一年换了别的，大家竟然都不高兴。第二年红烧肉又端上了桌，从此再也没人敢换。 / 同发巷每年腊月都要摆一次长桌宴，二十多户人家各出一道菜，苏念安家年年做红烧肉，有一年换了别的，大家竟然都不高兴。第二年红烧肉又端上了桌，从此再也没人敢换。 | content-word: the same content-word mismatch was heard twice |
| s104 | content-word | corrupted | rejected | 0.9326 | 赵元叔七十岁那年卖掉了驴车，买了一辆三轮车，可是他骑得很慢，比驴子还快。镇上的人都说，他其实是舍不得那头驴，因为他每天还是绕路去看它一眼，有时候还给它带一根胡萝卜。 / 赵元叔七十岁那年卖掉了驴车，买了一辆三轮车，可是他骑得很慢，比驴子还快。镇上的人都说，他其实是舍不得那头驴，因为他每天还是绕路去看它一眼，有时候还给它带一根胡萝卜。 | content-word: the same content-word mismatch was heard twice |
| s105 | good | good | rejected | 0.9872 | 林秋河的外婆养了十一只鸡，每只都有名字。她说叫名字的时候鸡会答应，孙辈们都不信，直到有一天亲眼看见，一只叫小黑的母鸡听到名字就跑了过来，孩子们这才服了气。 / 林秋河的外婆养了十一只鸡，每只都有名字。她说叫名字的时候鸡会答应，孙辈们都不信，直到有一天亲眼看见，一只叫小黑的母鸡听到名字就跑了过来，孩子们这才服了气。 | content-word: the same content-word mismatch was heard twice |
| s106 | good | good | rejected | 0.9487 | 钱大夏第一次去北岭爬山那年才九岁。他走在最前面，下山的时候却哭着要人背。后来，他成了镇上最有名的向导。每次带孩子上山，他都会讲这个故事，孩子们每次都听得哈哈大笑。 / 钱大夏第一次去北岭爬山那年才九岁。他走在最前面，下山的时候却哭着要人背。后来，他成了镇上最有名的向导。每次带孩子上山，他都会讲这个故事，孩子们每次都听得哈哈大笑。 | content-word: the same content-word mismatch was heard twice |
| s111 | name-change | corrupted | rejected | 0.9268 | 沈青吾写了一本关于北林野花的小书，书里记了六十种花，每一种都配了他自己画的图，印了五百本，很快就送完了。后来有人出高价想买一本，他也拿不出来。 / 沈青吾写了一本关于北林野花的小书，书里记了六十种花，每一种都配了他自己画的图，印了五百本，很快就送完了。后来有人出高价想买一本，他也拿不出来。 | content-word: the same content-word mismatch was heard twice |
| s112 | good | good | rejected | 0.9615 | 去北岭的山路又长又难走，一下雨就满地泥水，所以赶车的赵云叔对比一袋面粉重的东西，一律收双倍的运费。可是遇到送药的人，他一分钱也不收，这是他自己定下的规矩。 / 去北岭的山路又长又难走，一下雨就满地泥水，所以赶车的赵云叔对比一袋面粉重的东西，一律收双倍的运费。可是遇到送药的人，他一分钱也不收，这是他自己定下的规矩。 | content-word: the same content-word mismatch was heard twice |
| s113 | clause-drop | corrupted | rejected | 0.8261 | 顾长庚年轻的时候在白沙渡撑船，后来改行卖鱼具。他说卖鱼具比撑船赚得多，可是远远没有撑船快活，所以每到星期天，在河上撑一个上午。 | protected-token: deletion: expected "他还是会借一条船" / heard "" |
| s114 | word-swap | corrupted | rejected | 0.9419 | 原先的石板路一共有九百块石板，何小板带着学生一块一块数过，数到最后少了一块，大家找了一个下午，没也找到。第二天才发现，是有人把它搬回家垫了水缸。 / 原先的石板路一共有九百块石板，何小板带着学生一块一块数过，数到最后少了一块，大家找了一个下午，没也找到。第二天才发现，是有人把它搬回家垫了水缸。 | content-word: the same content-word mismatch was heard twice |
| s115 | name-change | corrupted | rejected | 0.9651 | 云溪的石板路一共有九百块石板。送快艇带着学生一块一块数过，数到最后少了一块。大家找了一个下午，也没找到。第二天才发现，是有人把它搬回家垫了水缸。 / 云溪的石板路一共有九百块石板。送快艇带着学生一块一块数过，数到最后少了一块。大家找了一个下午，也没找到。第二天才发现，是有人把它搬回家垫了水缸。 | content-word: the same content-word mismatch was heard twice |
| s116 | good | good | rejected | 0.9342 | 康之秋学做豆腐学了三年，头两年做出来的不是太硬就是太软。到第三年春天，他做的豆腐终于能在筷子上立住了。他把第一块好豆腐送给了教他的老师傅。 / 康之秋学做豆腐学了三年，头两年做出来的不是太硬就是太软。到第三年春天，他做的豆腐终于能在筷子上立住了。他把第一块好豆腐送给了教他的老师傅。 | content-word: the same content-word mismatch was heard twice |
| s119 | content-word | corrupted | rejected | 0.9740 | 丰桥镇的戏台每年春天唱五天戏，前两天的观众总是很少。到了最后一天，连隔壁镇的人都搬着板凳来占位子。台上唱的是什么，反倒没几个人在意，大家要的只是那份热闹。 / 丰桥镇的戏台每年春天唱五天戏，前两天的观众总是很少。到了最后一天，连隔壁镇的人都搬着板凳来占位子。台上唱的是什么，反倒没几个人在意，大家要的只是那份热闹。 | content-word: the same content-word mismatch was heard twice |

## Per-type results (held out)

| Kind | Accepted | Rate | Wilson 95% | Scores |
|---|---|---|---|---|
| good | 3 of 24 | 12.5% | 0.04–0.31 | 0.8356–1.0000 |
| word-swap | 0 of 4 | 0.0% | 0.00–0.49 | 0.9213–0.9740 |
| clause-drop | 0 of 4 | 0.0% | 0.00–0.49 | 0.7805–0.9157 |
| preamble | 0 of 4 | 0.0% | 0.00–0.49 | 0.8406–0.8750 |
| number-change | 0 of 4 | 0.0% | 0.00–0.49 | 0.8732–0.9589 |
| name-change | 0 of 4 | 0.0% | 0.00–0.49 | 0.9268–0.9651 |
| content-word | 0 of 4 | 0.0% | 0.00–0.49 | 0.9326–0.9740 |

## Per-type results (proposal part)

| Kind | Accepted | Rate | Wilson 95% | Scores |
|---|---|---|---|---|
| good | 10 of 36 | 27.8% | 0.16–0.44 | 0.9041–1.0000 |
| word-swap | 0 of 6 | 0.0% | 0.00–0.39 | 0.9155–0.9710 |
| clause-drop | 0 of 6 | 0.0% | 0.00–0.39 | 0.6133–0.9114 |
| preamble | 0 of 6 | 0.0% | 0.00–0.39 | 0.8406–0.9103 |
| number-change | 0 of 6 | 0.0% | 0.00–0.39 | 0.8333–0.9481 |
| name-change | 0 of 6 | 0.0% | 0.00–0.39 | 0.9211–0.9620 |
| content-word | 0 of 6 | 0.0% | 0.00–0.39 | 0.9342–0.9875 |

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

