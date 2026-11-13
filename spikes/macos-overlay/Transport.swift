import Foundation

// All state and delegate callbacks run on the main queue; one task at a time.
final class DashboardTransport: NSObject, URLSessionDataDelegate {
    static let endpoint = URL(string: "http://127.0.0.1:8080/api/dashboard")!
    static let limit = 1_048_576
    private lazy var session = URLSession(configuration: Self.configuration(), delegate: self, delegateQueue: .main)
    private var completion: ((Data?) -> Void)?
    private var body = Data()
    private var rejected = false
    private var accepted = false

    static func request() -> URLRequest {
        var request = URLRequest(url: endpoint, cachePolicy: .reloadIgnoringLocalCacheData, timeoutInterval: 3)
        request.setValue("http://127.0.0.1:8080", forHTTPHeaderField: "Origin")
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        request.httpShouldHandleCookies = false
        return request
    }

    static func configuration() -> URLSessionConfiguration {
        let config = URLSessionConfiguration.ephemeral
        config.connectionProxyDictionary = [:]
        config.urlCache = nil
        config.requestCachePolicy = .reloadIgnoringLocalCacheData
        config.httpCookieStorage = nil
        config.httpShouldSetCookies = false
        config.urlCredentialStorage = nil
        config.timeoutIntervalForRequest = 3
        config.timeoutIntervalForResource = 5
        config.waitsForConnectivity = false
        config.httpMaximumConnectionsPerHost = 1
        return config
    }

    static func accepts(_ response: HTTPURLResponse, bytes: Int) -> Bool {
        response.url == endpoint && response.statusCode == 200 && response.mimeType == "application/json" &&
            response.expectedContentLength <= Int64(limit) && (0...limit).contains(bytes)
    }

    func fetch(_ callback: @escaping (Data?) -> Void) {
        precondition(Thread.isMainThread && completion == nil, "Sequential main-queue polling only")
        completion = callback
        body = Data()
        rejected = false
        accepted = false
        session.dataTask(with: Self.request()).resume()
    }

    func stop() { session.invalidateAndCancel() }

    func urlSession(_ session: URLSession, task: URLSessionTask,
                    willPerformHTTPRedirection response: HTTPURLResponse,
                    newRequest request: URLRequest,
                    completionHandler: @escaping (URLRequest?) -> Void) {
        rejected = true // Even a same-loopback redirect may not activate advice.
        completionHandler(nil)
    }

    func urlSession(_ session: URLSession, task: URLSessionTask,
                    didReceive challenge: URLAuthenticationChallenge,
                    completionHandler: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
        rejected = true
        completionHandler(.cancelAuthenticationChallenge, nil)
    }

    func urlSession(_ session: URLSession, dataTask: URLSessionDataTask, didReceive response: URLResponse,
                    completionHandler: @escaping (URLSession.ResponseDisposition) -> Void) {
        accepted = !rejected && (response as? HTTPURLResponse).map { Self.accepts($0, bytes: 0) } == true
        rejected = !accepted
        completionHandler(accepted ? .allow : .cancel)
    }

    func urlSession(_ session: URLSession, dataTask: URLSessionDataTask, didReceive data: Data) {
        guard !rejected && data.count <= Self.limit - body.count else {
            rejected = true
            body = Data()
            dataTask.cancel()
            return
        }
        body.append(data)
    }

    func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: Error?) {
        let result = error == nil && accepted && !rejected ? body : nil
        body = Data()
        let callback = completion
        completion = nil
        callback?(result)
    }
}
