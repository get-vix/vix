import Foundation
import Testing
@testable import VixMacCore

@Test func plainTextIsOneSegment() {
    let segs = splitMarkdown("just some **prose** here")
    #expect(segs.count == 1)
    #expect(segs.first == .text("just some **prose** here"))
}

@Test func textCodeTextSplitsIntoThree() {
    let input = """
    Here is code:
    ```swift
    let x = 1
    print(x)
    ```
    Done.
    """
    let segs = splitMarkdown(input)
    #expect(segs.count == 3)
    #expect(segs[0] == .text("Here is code:"))
    #expect(segs[1] == .code(language: "swift", code: "let x = 1\nprint(x)"))
    #expect(segs[2] == .text("Done."))
}

@Test func codeWithoutLanguageHasNilLanguage() {
    let segs = splitMarkdown("```\nplain\n```")
    #expect(segs == [.code(language: nil, code: "plain")])
}

@Test func unterminatedFenceStillYieldsCode() {
    // Mid-stream: closing fence hasn't arrived yet.
    let segs = splitMarkdown("intro\n```python\nprint(1)")
    #expect(segs.count == 2)
    #expect(segs[0] == .text("intro"))
    #expect(segs[1] == .code(language: "python", code: "print(1)"))
}

@Test func inlineBackticksAreNotCodeBlocks() {
    let segs = splitMarkdown("use `let x = 1` inline")
    #expect(segs == [.text("use `let x = 1` inline")])
}

@Test func emptyInputYieldsNoSegments() {
    #expect(splitMarkdown("").isEmpty)
    #expect(splitMarkdown("   \n  ").isEmpty)
}
