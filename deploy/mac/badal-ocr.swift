// badal-ocr — read text from an image using Apple's Vision framework.
//
// Used instead of a downloaded OCR model because it is already on the machine,
// needs no Python, no weights, and no GPU memory, and is markedly better on
// skewed phone photos than the small open models that would fit alongside
// everything else.
//
// Usage: badal-ocr <image-path>
// Writes recognised text to stdout, one line per observation.
// Exit 0 with empty output means "no text found", which is a valid answer.

import Foundation
import Vision
import CoreImage

let args = CommandLine.arguments
guard args.count > 1 else {
    FileHandle.standardError.write("usage: badal-ocr <image-path>\n".data(using: .utf8)!)
    exit(2)
}

let url = URL(fileURLWithPath: args[1])
guard let ci = CIImage(contentsOf: url) else {
    FileHandle.standardError.write("cannot read image\n".data(using: .utf8)!)
    exit(3)
}

let request = VNRecognizeTextRequest()
// Accurate over fast: a poster is read once and the date has to be right.
request.recognitionLevel = .accurate
// Language correction helps prose and hurts codes, dates and venue names.
// Those are exactly what we are here for, so it stays off.
request.usesLanguageCorrection = false
request.recognitionLanguages = ["en-US"]

let handler = VNImageRequestHandler(ciImage: ci, options: [:])
do {
    try handler.perform([request])
} catch {
    FileHandle.standardError.write("vision failed: \(error)\n".data(using: .utf8)!)
    exit(4)
}

guard let observations = request.results else { exit(0) }

// Vision returns observations in no guaranteed reading order. Sort top-to-
// bottom then left-to-right, or a two-column poster comes out interleaved and
// unreadable.
let sorted = observations.sorted { a, b in
    let ay = a.boundingBox.origin.y, by = b.boundingBox.origin.y
    if abs(ay - by) > 0.012 { return ay > by }        // higher on the page first
    return a.boundingBox.origin.x < b.boundingBox.origin.x
}

var out: [String] = []
for o in sorted {
    guard let best = o.topCandidates(1).first else { continue }
    // Drop very low-confidence fragments: they are usually texture read as
    // letters, and feeding noise to the extractor produces invented dates.
    if best.confidence < 0.3 { continue }
    out.append(best.string)
}

print(out.joined(separator: "\n"))
