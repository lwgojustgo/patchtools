#ifndef C_WRAPPER_H_
#define C_WRAPPER_H_

#include <stdint.h>
#include <stdbool.h>

#ifdef WIN32
	#define DLLEXPORT __declspec(dllexport)
	#define DLLIMPORT __declspec(dllimport)
#else
	#define DLLEXPORT __attribute__((visibility("default")))
	#define DLLIMPORT __attribute__((visibility("default")))
#endif

#ifdef PluginWrapCaller_EXPORTS
	#define CALLERDLL_API DLLEXPORT
#else
#ifdef FORTEST
	#define CALLERDLL_API
#else
	#define CALLERDLL_API DLLIMPORT
#endif // FORTEST
#endif

// __cplusplus gets defined when a C++ compiler processes the file
#ifdef __cplusplus
// extern "C" is needed so the C++ compiler exports the symbols without name
// manging.
extern "C" {
#endif

	typedef struct _QCoreApplication_t
	{
		int unused;
	} QCoreApplication_t;

	CALLERDLL_API void *newCaller();
	CALLERDLL_API void deleteCaller(void *pCaller);
	CALLERDLL_API bool initializeCaller(void *pCaller, const char* dllFullPath, const char* configFullPath);	
	CALLERDLL_API char* procCaller(void *pCaller, const char* bodyBuffer, unsigned long dwCommand, const char* traceID, unsigned char* dataType, char** httpHeader);
	CALLERDLL_API void freeMem(char* p);
	CALLERDLL_API void setLogger(void *pCaller, const char* logPath, const char* name);
	CALLERDLL_API void enableDebugLogger(void *pCaller, unsigned long flag);

	CALLERDLL_API QCoreApplication_t *newApplication(bool isGui);
	CALLERDLL_API void deleteApplication(QCoreApplication_t *app);
	CALLERDLL_API int applicationExec(QCoreApplication_t *app);
	CALLERDLL_API int exitProgram(int code);

#ifdef __cplusplus
}
#endif

#endif

